package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"garp/eval/internal/corpus"
	"garp/eval/internal/queryset"
	"garp/eval/internal/report"
	"garp/eval/internal/systems"
)

func saveJSON(t *testing.T, p string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckKind(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"a.txt": "alpha beta file_name", "b.txt": "alpha gamma file_name",
		"bad.docx": "not a zip", "m.html": "<p>Olk10SideProps chunk</p>",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := systems.BuildIndex(dir, ".", []string{"a.txt", "b.txt", "bad.docx", "m.html"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind  string
		args  []string
		gold  []string
		valid bool
	}{
		{"single", []string{"alpha", "--strict"}, nil, true},
		{"single", []string{"alpha", "beta"}, nil, false},
		{"single", []string{"beta"}, nil, false},
		{"single", []string{"missing"}, []string{"bad.docx"}, true},
		{"single", []string{"alpha"}, []string{"a.txt", "bad.docx"}, true},
		{"single", []string{"Olk10SideProps"}, []string{"m.html"}, true},
		{"proximity", []string{"Olk10SideProps", "chunk"}, []string{"m.html"}, true},
		{"synonym", []string{"Olk10SideProps", "chunk"}, []string{"m.html"}, false},
		{"proximity", []string{"ALPHA", "file_na*", "--distance", "300"}, nil, true},
		{"proximity", []string{"alpha", "beta"}, nil, false},
		{"synonym", []string{"alpha", "missing"}, nil, true},
		{"synonym", []string{"beta", "gamma"}, nil, false},
		{"synonym", []string{"alpha", "file_name"}, nil, false},
	} {
		if tc.gold == nil {
			tc.gold = []string{"a.txt", "b.txt"}
		}
		q := queryset.Query{ID: "kind-check", Kind: tc.kind, Args: tc.args}
		for _, file := range tc.gold {
			q.Gold = append(q.Gold, queryset.Gold{File: file})
		}
		if err := checkKind(q, idx); (err == nil) != tc.valid {
			t.Errorf("%s %v %v: %v", tc.kind, tc.args, tc.gold, err)
		}
	}
}

func TestScore(t *testing.T) {
	q := queryset.Query{Gold: []queryset.Gold{{File: "a.txt", Lines: []int{1, 2}}, {File: "a.txt", Lines: []int{3, 4}}, {File: "missing.txt"}}, JudgedNonrelevant: []string{"b.txt"}}
	r := systems.Run{Ranking: []string{"b.txt", "a.txt", "c.txt"}, Stdout: []byte(`{"results":[{"file":"b.txt"},{"file":"a.txt","excerpts":[{"start_line":1}]},{"file":"c.txt"}]}`)}
	for _, system := range []string{"candidate", "previous", "bm25", "rg"} {
		m, err := score(q, system, r, 2000)
		if err != nil || !slices.Equal(m.GoldRanks, []int{2, 0}) || m.Unjudged10 != 1 {
			t.Fatalf("%s: %+v %v", system, m, err)
		}
		if (m.Tokens == nil) != (system == "bm25") {
			t.Fatalf("tokens availability: %s", system)
		}
		if system == "candidate" || system == "previous" {
			if m.HitN == nil || *m.HitN != 1.0/3 {
				t.Fatalf("gold ranges: %+v", m)
			}
		} else if m.HitN != nil {
			t.Fatalf("hit availability: %s", system)
		}
	}
	m, err := score(q, "candidate", systems.Run{Err: "exit status 1"}, 2000)
	if err != nil || m.Error == "" || m.Returned != 0 || *m.HitN != 0 {
		t.Fatalf("scored process failure: %+v %v", m, err)
	}
}

func TestRun(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "garp")
	if b, err := exec.Command("go", "build", "-o", bin, "garp").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, b)
	}
	old := minimums
	minimums = queryset.Minimums{}
	t.Cleanup(func() { minimums = old })
	for _, c := range []string{"docs", "code"} {
		root := "."
		if c == "code" {
			root = "garp"
		}
		var files []corpus.File
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			text := []byte("alpha beta gamma\n")
			rel := filepath.ToSlash(filepath.Join(root, name))
			p := filepath.Join(dir, "cache", c, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, text, 0600); err != nil {
				t.Fatal(err)
			}
			files = append(files, corpus.File{Path: rel, Size: int64(len(text)), SHA256: fmt.Sprintf("%x", sha256.Sum256(text))})
		}
		saveJSON(t, filepath.Join(dir, "manifests", c+".json"), corpus.Manifest{Corpus: c, Files: files})
		saveJSON(t, filepath.Join(dir, "queries", c+".json"), queryset.Set{Corpus: c, TokenBudget: 2000, Queries: []queryset.Query{
			{ID: c + "-single", Root: root, Kind: "single", Need: "Find alpha", Args: []string{"alpha"}, Gold: []queryset.Gold{{File: "a.txt", Lines: []int{1, 1}}}, JudgedNonrelevant: []string{"b.txt"}},
			{ID: c + "-proximity", Root: root, Kind: "proximity", Need: "Find both", Args: []string{"ALPHA", "bet*"}, Gold: []queryset.Gold{{File: "a.txt"}}},
			{ID: c + "-synonym", Root: root, Kind: "synonym", Need: "Find equivalent", Args: []string{"alpha", "missing"}, Gold: []queryset.Gold{{File: "a.txt"}}},
		}})
	}
	prefix := filepath.Join(dir, "results", "latest")
	args := []string{"-cache", filepath.Join(dir, "cache"), "-queries", filepath.Join(dir, "queries"), "-manifests", filepath.Join(dir, "manifests"), "-candidate", bin, "-previous", bin, "-previous-ref", "explicit-ref", "-out", prefix, "-no-gate"}
	invoke := func(a []string, want int, message string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		if got := run(a, &out, &stderr); got != want || !strings.Contains(stderr.String(), message) {
			t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", got, want, out.String(), stderr.String())
		}
		if want == 0 && slices.Contains(a, "-pool") && stderr.Len() != 0 {
			t.Fatalf("blind pool diagnostics: %s", stderr.String())
		}
		return out.String()
	}
	first := invoke(args, 0, "")
	a, err := os.ReadFile(prefix + ".json")
	if err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(prefix + ".md")
	if err != nil || string(md) != first {
		t.Fatalf("markdown differs: %v", err)
	}
	invoke(args, 0, "")
	b, err := os.ReadFile(prefix + ".json")
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("JSON nondeterminism: %v", err)
	}
	for _, s := range []string{dir, "wall", "timing"} {
		if bytes.Contains(a, []byte(s)) {
			t.Errorf("JSON contains %q", s)
		}
	}
	var result report.Results
	if err := json.Unmarshal(a, &result); err != nil {
		t.Fatal(err)
	}
	if result.Systems.Previous.Ref != "explicit-ref" || !strings.HasPrefix(result.Systems.Candidate.Version, "garp v") {
		t.Fatalf("metadata: %+v", result.Systems)
	}
	for _, c := range []string{"docs", "code"} {
		if len(result.Corpora[c].Aggregate) != 4 || len(result.Corpora[c].ByKind) != 3 {
			t.Fatalf("missing systems/kinds: %+v", result.Corpora[c])
		}
		q := result.Corpora[c].Queries[0].PerSystem["candidate"]
		if q.Returned != 3 || q.Unjudged10 != 1 || q.HitN == nil || *q.HitN != 1 {
			t.Fatalf("metrics: %+v", q)
		}
	}
	t.Run("gate", func(t *testing.T) {
		// Two of the three queries hit at rank 1: the saved 2/3 rounds up.
		if result.Corpora["docs"].Aggregate["candidate"].MRR10 != 0.666667 {
			t.Fatal("fixture must exercise upward baseline rounding")
		}
		baselinePath := filepath.Join(dir, "baseline.json")
		for _, tc := range []struct {
			name, message string
			mutate        func(*report.Results)
			status        int
		}{
			{name: "equal", status: 0},
			{name: "regression", status: 1, mutate: func(r *report.Results) {
				s := r.Corpora["docs"].Aggregate["candidate"]
				s.R5 += 0.01
				r.Corpora["docs"].Aggregate["candidate"] = s
			}},
			{name: "within tolerance", status: 0, mutate: func(r *report.Results) {
				s := r.Corpora["docs"].Aggregate["candidate"]
				s.R5 += 1e-10
				r.Corpora["docs"].Aggregate["candidate"] = s
			}},
			{name: "query stale", status: 2, message: "baseline stale: run make eval-baseline", mutate: func(r *report.Results) { r.Harness.QuerySHA256["docs"] = "changed" }},
			{name: "manifest stale", status: 2, message: "baseline stale: run make eval-baseline", mutate: func(r *report.Results) { r.Harness.ManifestSHA256["code"] = "changed" }},
			{name: "ref stale", status: 2, message: "baseline stale: recorded for ref", mutate: func(r *report.Results) { r.Systems.Previous.Ref = "other-ref" }},
			{name: "version stale", status: 2, message: "baseline stale: recorded for ref", mutate: func(r *report.Results) { r.Systems.Previous.Version = "other-version" }},
			{name: "tokenizer stale", status: 2, message: "baseline stale: recorded for tokenizer", mutate: func(r *report.Results) { r.Harness.Tokenizer = "other-tokenizer" }},
			{name: "empty tokenizer", status: 2, message: "baseline stale: recorded for tokenizer", mutate: func(r *report.Results) { r.Harness.Tokenizer = "" }},
			{name: "schema stale", status: 2, message: "baseline stale: recorded for schema", mutate: func(r *report.Results) { r.Schema = 2 }},
			{name: "zero schema", status: 2, message: "baseline stale: recorded for schema", mutate: func(r *report.Results) { r.Schema = 0 }},
			{name: "missing candidate", status: 2, message: "baseline missing candidate metrics: docs", mutate: func(r *report.Results) { delete(r.Corpora["docs"].Aggregate, "candidate") }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var baseline report.Results
				if err := json.Unmarshal(a, &baseline); err != nil {
					t.Fatal(err)
				}
				if tc.mutate != nil {
					tc.mutate(&baseline)
				}
				saveJSON(t, baselinePath, baseline)
				gatePrefix := filepath.Join(dir, tc.name, "latest")
				gated := append(args, "-no-gate=false", "-baseline", baselinePath, "-out", gatePrefix)
				output := invoke(gated, tc.status, tc.message)
				if tc.status == 2 {
					if _, err := os.Stat(gatePrefix + ".json"); !os.IsNotExist(err) || output != "" {
						t.Fatal("invalid baseline wrote results")
					}
				} else {
					want := "### regressions vs " + baselinePath + ": none"
					if tc.status == 1 {
						want = "docs r5: 0.676667 -> 0.666667"
					}
					md, err := os.ReadFile(gatePrefix + ".md")
					if err != nil || string(md) != output || !strings.Contains(output, want) || strings.Count(output, "### regressions vs ") != 1 {
						t.Fatalf("gate report: %v\n%s", err, output)
					}
				}
				// Recording never gates, including against a stale baseline.
				if output := invoke(append(gated, "-no-gate"), 0, ""); strings.Contains(output, "### regressions vs ") {
					t.Fatal("-no-gate printed a gate decision")
				}
			})
		}
	})
	t.Run("pool", func(t *testing.T) {
		poolPrefix := filepath.Join(dir, "pool")
		pool := invoke(append(args, "-pool", "docs-single,code-single", "-previous-ref", "", "-out", poolPrefix), 0, "")
		for _, s := range []string{"gold", "judged_nonrelevant", "candidate", "previous", "bm25", "rg", "score"} {
			if regexp.MustCompile(`\b` + s + `\b`).MatchString(pool) {
				t.Errorf("pool reveals %q: %s", s, pool)
			}
		}
		if strings.Count(pool, "a.txt\nb.txt\nc.txt") != 2 {
			t.Fatalf("pool union/order: %s", pool)
		}
		if _, err := os.Stat(poolPrefix + ".json"); !os.IsNotExist(err) {
			t.Fatal("pool wrote results")
		}
	})
	t.Run("missing rg", func(t *testing.T) { t.Setenv("PATH", ""); invoke(args, 2, "rg") })
	invoke(append(args, "-previous-ref", ""), 2, "-previous-ref")
	invoke(append(args, "-baseline", prefix+".json"), 0, "")
	invoke(append(args, "-no-gate=false", "-baseline", filepath.Join(dir, "missing")), 2, "baseline missing: run make eval-baseline")
	t.Run("unexpected corpus file", func(t *testing.T) {
		p := filepath.Join(dir, "cache", "docs", "extra.txt")
		if err := os.WriteFile(p, []byte("alpha"), 0600); err != nil {
			t.Fatal(err)
		}
		invoke(args, 2, "unexpected corpus file: docs/extra.txt")
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("bad kind", func(t *testing.T) {
		p := filepath.Join(dir, "queries", "docs.json")
		s, _, err := queryset.Load(p)
		if err != nil {
			t.Fatal(err)
		}
		original := s.Queries[0].Kind
		s.Queries[0].Kind = "proximity"
		saveJSON(t, p, s)
		invoke(args, 2, "docs-single")
		s.Queries[0].Kind = original
		saveJSON(t, p, s)
	})
	t.Run("corrupt corpus", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "cache", "docs", "a.txt"), []byte("Alpha beta gamma\n"), 0600); err != nil {
			t.Fatal(err)
		}
		invoke(args, 2, "sha256 mismatch: docs/a.txt")
	})
}
