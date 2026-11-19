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
		"bad.docx": "not a zip", "m.html": "<p>Olk10SideProps chunk sha256</p>",
		"long.txt": "deployment",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := systems.BuildIndex(dir, ".", []string{"a.txt", "b.txt", "bad.docx", "m.html", "long.txt"})
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
		{"single", []string{"alpha|beta", "--partial"}, nil, false},
		{"proximity", []string{"alpha|gamma", "beta", "--partial"}, nil, false},
		{"synonym", []string{"alpha", "missing|absent"}, nil, false},
		{"single", []string{"alpha", "beta"}, nil, false},
		{"single", []string{"beta"}, nil, false},
		{"single", []string{"deploy", "--partial=prefix"}, []string{"long.txt"}, true},
		{"single", []string{"deploy", "--partial"}, []string{"long.txt"}, true},
		{"single", []string{"deploy", "--partial=contains"}, []string{"long.txt"}, true},
		{"single", []string{"deploy", "--smart-forms"}, []string{"long.txt"}, true},
		{"single", []string{"deploy"}, []string{"long.txt"}, false},
		{"single", []string{"--partial"}, nil, false},
		{"single", []string{"alpha", "beta", "--partial"}, nil, false},
		{"single", []string{"missing"}, []string{"bad.docx"}, true},
		{"single", []string{"alpha"}, []string{"a.txt", "bad.docx"}, true},
		{"single", []string{"Olk10SideProps"}, []string{"m.html"}, true},
		{"single", []string{"SHA256"}, []string{"m.html"}, true},
		{"single", []string{"SHA512"}, []string{"m.html"}, false},
		{"proximity", []string{"SHA256", "chunk"}, []string{"m.html"}, true},
		{"synonym", []string{"SHA256", "missing"}, []string{"m.html"}, true},
		{"synonym", []string{"SHA256", "chunk"}, []string{"m.html"}, false},
		{"proximity", []string{"Olk10SideProps", "chunk"}, []string{"m.html"}, true},
		{"synonym", []string{"Olk10SideProps", "chunk"}, []string{"m.html"}, false},
		{"proximity", []string{"ALPHA", "file_na*", "--distance", "300"}, nil, true},
		{"proximity", []string{"alpha", "beta"}, nil, false},
		{"proximity", []string{"alpha", "missing", "--partial"}, nil, true},
		{"proximity", []string{"alpha", "--partial"}, nil, false},
		{"synonym", []string{"alpha", "missing"}, nil, true},
		{"synonym", []string{"beta", "gamma"}, nil, false},
		{"synonym", []string{"alpha", "file_name"}, nil, false},
		{"synonym", []string{"alpha", "file_name", "--smart-forms"}, nil, true},
		{"synonym", []string{"alpha", "--smart-forms"}, nil, false},
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

func TestLoadSynonymOROverlay(t *testing.T) {
	old := minimums
	minimums = queryset.Minimums{}
	t.Cleanup(func() { minimums = old })
	s := queryset.Set{Corpus: "code", TokenBudget: 100, Queries: []queryset.Query{{ID: "q", Root: "garp", Kind: "synonym", Args: []string{"alpha", "missing", "--distance", "300"}, Gold: []queryset.Gold{{File: "a.txt"}}, JudgedNonrelevant: []string{"b.txt"}}}}
	for _, tc := range []struct {
		name, kind  string
		args, extra []string
		valid       bool
	}{
		{"valid", "synonym", nil, []string{"c.txt"}, true},
		{"flags", "synonym", []string{"alpha", "missing|beta", "--distance", "301"}, nil, false},
		{"slots", "synonym", []string{"alpha|beta", "--distance", "300"}, nil, false},
		{"first alternative", "synonym", []string{"beta|alpha", "missing", "--distance", "300"}, nil, false},
		{"no expansion", "synonym", []string{"alpha", "missing", "--distance", "300"}, nil, false},
		{"wildcard", "synonym", []string{"alpha", "missing|beta*", "--distance", "300"}, nil, false},
		{"malformed", "synonym", []string{"alpha", "missing||beta", "--distance", "300"}, nil, false},
		{"proximity", "proximity", nil, nil, false},
		{"single", "single", nil, nil, false},
		{"gold collision", "synonym", nil, []string{"a.txt"}, false},
		{"judged collision", "synonym", nil, []string{"b.txt"}, false},
		{"duplicate", "synonym", nil, []string{"c.txt", "c.txt"}, false},
		{"unsorted", "synonym", nil, []string{"d.txt", "c.txt"}, false},
		{"not manifested", "synonym", nil, []string{"missing.txt"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.Queries[0].Kind = tc.kind
			if tc.args == nil {
				tc.args = []string{"alpha", "missing|beta", "--distance", "300"}
			}
			p := filepath.Join(t.TempDir(), "overlay.json")
			saveJSON(t, p, map[string]map[string]synonymOREntry{"code": {"q": {Args: tc.args, ExtraJudgedNonrelevant: tc.extra}}})
			sets, hash, err := loadSynonymOROverlay(p, map[string]queryset.Set{"code": s}, map[string][]string{"code": {"garp/a.txt", "garp/b.txt", "garp/c.txt", "garp/d.txt"}})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if tc.valid && (hash == "" || !slices.Equal(sets["code"].Queries[0].JudgedNonrelevant, []string{"b.txt", "c.txt"}) || s.Queries[0].Args[1] != "missing" || len(s.Queries[0].JudgedNonrelevant) != 1) {
				t.Fatal("overlay mutated base or lost judgments")
			}
		})
	}
	for _, raw := range []string{`{"code":{"unknown":{"args":["a|b"]}}}`, `{"unknown":{}}`, `{} {}`, `{"code":{"q":{"gold":[],"args":["alpha","missing|beta","--distance","300"]}}}`} {
		p := filepath.Join(t.TempDir(), "overlay.json")
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadSynonymOROverlay(p, map[string]queryset.Set{"code": s}, nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestLoadSynonymORKeyGaps(t *testing.T) {
	s := queryset.Set{Corpus: "code", Queries: []queryset.Query{{ID: "q", Root: "garp", Kind: "synonym", Need: "Find equivalent", Args: []string{"alpha|beta", "missing"}, Gold: []queryset.Gold{{File: "a.txt"}}, JudgedNonrelevant: []string{"b.txt"}}}}
	for _, tc := range []struct {
		name   string
		mutate func(*synonymORKeyGap)
		valid  bool
	}{
		{"valid", nil, true},
		{"unknown id", func(g *synonymORKeyGap) { g.ID = "unknown" }, false},
		{"need drift", func(g *synonymORKeyGap) { g.Need = "other" }, false},
		{"no evidence", func(g *synonymORKeyGap) { g.Evidence = " " }, false},
		{"gold", func(g *synonymORKeyGap) { g.Path = "a.txt" }, false},
		{"nonrelevant", func(g *synonymORKeyGap) { g.Path = "b.txt" }, false},
		{"unmanifested", func(g *synonymORKeyGap) { g.Path = "missing.txt" }, false},
		{"traversal", func(g *synonymORKeyGap) { g.Path = "../garp/c.txt" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := synonymORKeyGap{ID: "q", Path: "c.txt", Need: s.Queries[0].Need, Evidence: "c.txt:1 answers the need"}
			if tc.mutate != nil {
				tc.mutate(&g)
			}
			p := filepath.Join(t.TempDir(), "gaps.json")
			saveJSON(t, p, []synonymORKeyGap{g})
			gaps, hash, err := loadSynonymORKeyGaps(p, map[string]queryset.Set{"code": s}, map[string][]string{"code": {"garp/a.txt", "garp/b.txt", "garp/c.txt"}})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if tc.valid {
				if hash == "" || !slices.Equal(gaps["q"], []string{"c.txt"}) {
					t.Fatalf("gaps=%v hash=%s", gaps, hash)
				}
				saveJSON(t, p, []synonymORKeyGap{g, g})
				if _, _, err := loadSynonymORKeyGaps(p, map[string]queryset.Set{"code": s}, nil); err == nil {
					t.Fatal("accepted duplicate gap")
				}
			}
		})
	}
}

func TestSynonymORGate(t *testing.T) {
	boundedDrop := func(r *report.Results) {
		c := r.Corpora["code"]
		s := c.ByKind["synonym"]["candidate"]
		s.R1 -= .10
		c.ByKind["synonym"]["candidate"] = s
		s = c.Aggregate["candidate"]
		s.R1 -= .03
		c.Aggregate["candidate"] = s
		c.Queries = []report.Query{{ID: "code-g-008", Kind: "synonym", PerSystem: map[string]report.QueryMetrics{
			"previous": {GoldRanks: []int{1}}, "candidate": {GoldRanks: []int{2}},
		}}}
		r.Corpora["code"] = c
	}
	for _, tc := range []struct {
		name   string
		mutate func(*report.Results)
		fail   bool
	}{
		{"lift", nil, false},
		{"bounded rank tradeoff", boundedDrop, false},
		{"synonym cap exceeded", func(r *report.Results) {
			boundedDrop(r)
			s := r.Corpora["code"].ByKind["synonym"]["candidate"]
			s.R1 -= .000001
			r.Corpora["code"].ByKind["synonym"]["candidate"] = s
		}, true},
		{"aggregate cap exceeded", func(r *report.Results) {
			boundedDrop(r)
			s := r.Corpora["code"].Aggregate["candidate"]
			s.R1 -= .000001
			r.Corpora["code"].Aggregate["candidate"] = s
		}, true},
		{"rank proof required", func(r *report.Results) {
			boundedDrop(r)
			c := r.Corpora["code"]
			c.Queries = nil
			r.Corpora["code"] = c
		}, true},
		{"other query cannot qualify", func(r *report.Results) {
			boundedDrop(r)
			r.Corpora["code"].Queries[0].ID = "other"
		}, true},
		{"larger rank drop", func(r *report.Results) {
			boundedDrop(r)
			r.Corpora["code"].Queries[0].PerSystem["candidate"] = report.QueryMetrics{GoldRanks: []int{3}}
		}, true},
		{"aggregate mrr still guarded", func(r *report.Results) {
			boundedDrop(r)
			s := r.Corpora["code"].Aggregate["candidate"]
			s.MRR10 -= .01
			r.Corpora["code"].Aggregate["candidate"] = s
		}, true},
		{"synonym r5 still guarded", func(r *report.Results) {
			boundedDrop(r)
			s := r.Corpora["code"].ByKind["synonym"]["candidate"]
			s.R5 -= .01
			r.Corpora["code"].ByKind["synonym"]["candidate"] = s
		}, true},
		{"tradeoff requires lift", func(r *report.Results) {
			boundedDrop(r)
			s := r.Corpora["code"].ByKind["synonym"]["candidate"]
			s.R10 = r.Corpora["code"].ByKind["synonym"]["previous"].R10
			r.Corpora["code"].ByKind["synonym"]["candidate"] = s
		}, true},
		{"masked code query regression", func(r *report.Results) {
			c := r.Corpora["code"]
			c.Queries = []report.Query{{ID: "other", Kind: "synonym", PerSystem: map[string]report.QueryMetrics{
				"previous": {GoldRanks: []int{1}}, "candidate": {GoldRanks: []int{2}},
			}}}
			r.Corpora["code"] = c
		}, true},
		{"masked docs query regression", func(r *report.Results) {
			boundedDrop(r)
			c := r.Corpora["docs"]
			c.Queries = r.Corpora["code"].Queries
			r.Corpora["docs"] = c
		}, true},
		{"no lift code", func(r *report.Results) {
			r.Corpora["code"].ByKind["synonym"]["candidate"] = r.Corpora["code"].ByKind["synonym"]["previous"]
		}, true},
		{"unjudged rg", func(r *report.Results) {
			s := r.Corpora["docs"].Aggregate["rg"]
			s.Unjudged10 = 1
			r.Corpora["docs"].Aggregate["rg"] = s
		}, true},
		{"single drift", func(r *report.Results) {
			s := r.Corpora["docs"].ByKind["single"]["candidate"]
			s.R1 += .1
			r.Corpora["docs"].ByKind["single"]["candidate"] = s
		}, true},
		{"proximity drift", func(r *report.Results) {
			s := r.Corpora["code"].ByKind["proximity"]["candidate"]
			s.R10 -= .1
			r.Corpora["code"].ByKind["proximity"]["candidate"] = s
		}, true},
		{"aggregate regression", func(r *report.Results) {
			s := r.Corpora["docs"].Aggregate["candidate"]
			s.MRR10 -= .1
			r.Corpora["docs"].Aggregate["candidate"] = s
		}, true},
		{"missing hit", func(r *report.Results) {
			s := r.Corpora["docs"].ByKind["synonym"]["candidate"]
			s.HitN = nil
			r.Corpora["docs"].ByKind["synonym"]["candidate"] = s
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := report.Number(.2), report.Number(.4)
			base := report.Summary{R1: .2, R5: .2, R10: .2, MRR10: .2, HitN: &before}
			lift := report.Summary{R1: .2, R5: .2, R10: .4, MRR10: .2, HitN: &after}
			r := report.Results{Corpora: map[string]report.Corpus{}}
			for _, name := range []string{"docs", "code"} {
				r.Corpora[name] = report.Corpus{Aggregate: map[string]report.Summary{"candidate": lift, "previous": base, "bm25": {}, "rg": {}}, ByKind: map[string]map[string]report.Summary{"synonym": {"candidate": lift, "previous": base}, "single": {"candidate": base, "previous": base}, "proximity": {"candidate": base, "previous": base}}}
			}
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			if got := synonymORGate(r); (len(got) > 0) != tc.fail {
				t.Fatalf("fail=%v: %v", tc.fail, got)
			}
		})
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
		known, err := score(q, system, r, 2000, "c.txt")
		if err != nil || known.Unjudged10 != 0 || !slices.Equal(known.GoldRanks, m.GoldRanks) || (m.HitN != nil && *known.HitN != *m.HitN) {
			t.Fatalf("key gap changed gold credit: %+v %v", known, err)
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
	t.Run("synonym overlay", func(t *testing.T) {
		p := filepath.Join(dir, "overlay.json")
		gapFile := filepath.Join(dir, "overlay.key_gaps.json")
		saveJSON(t, gapFile, []synonymORKeyGap{})
		overlay := map[string]map[string]synonymOREntry{}
		for _, name := range []string{"docs", "code"} {
			queryPath := filepath.Join(dir, "queries", name+".json")
			s, _, err := queryset.Load(queryPath)
			if err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(queryPath)
			t.Cleanup(func() {
				if err := os.WriteFile(queryPath, original, 0600); err != nil {
					t.Error(err)
				}
			})
			for i := range s.Queries {
				s.Queries[i].JudgedNonrelevant = []string{"b.txt", "c.txt"}
			}
			saveJSON(t, queryPath, s)
			overlay[name] = map[string]synonymOREntry{name + "-synonym": {Args: []string{"alpha", "missing|beta"}}}
		}
		saveJSON(t, p, overlay)
		paired := append(slices.Clone(args), "-synonym-or", p, "-no-gate=false")
		invoke(append(slices.Clone(paired), "-baseline", prefix+".json"), 2, "-baseline cannot be combined with -synonym-or")
		output := invoke(paired, 0, "")
		if !strings.Contains(output, "### synonym-or gate vs previous (explicit-ref): none") {
			t.Fatal(output)
		}
		data, _ := os.ReadFile(prefix + ".json")
		var r report.Results
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		if r.Harness.QuerySHA256["synonym_or"] == "" {
			t.Fatal("missing overlay hash")
		}
		for _, name := range []string{"docs", "code"} {
			by := r.Corpora[name].ByKind["synonym"]
			if by["candidate"].R10 != 1 || by["previous"].R10 != 0 {
				t.Fatalf("pair did not separate args: %+v", by)
			}
			overlay[name][name+"-synonym"] = synonymOREntry{Args: []string{"alpha", "missing|absent"}}
		}
		if output := invoke(append(slices.Clone(paired), "-pool", "docs-synonym,code-synonym"), 0, ""); strings.Count(output, "a.txt\nb.txt\nc.txt") != 2 {
			t.Fatal(output)
		}
		saveJSON(t, p, overlay)
		invoke(paired, 1, "")
		invoke(append(paired, "-no-gate"), 0, "")
		// A registered gap is covered but not credited as gold; the query is
		// reported separately and cannot supply the strict synonym lift.
		queryPath := filepath.Join(dir, "queries", "code.json")
		s, _, err := queryset.Load(queryPath)
		if err != nil {
			t.Fatal(err)
		}
		s.Queries[2].JudgedNonrelevant = []string{"b.txt"}
		saveJSON(t, queryPath, s)
		saveJSON(t, gapFile, []synonymORKeyGap{{ID: "code-synonym", Path: "c.txt", Need: s.Queries[2].Need, Evidence: "c.txt:1"}})
		overlay["code"]["code-synonym"] = synonymOREntry{Args: []string{"alpha", "missing|beta"}}
		saveJSON(t, p, overlay)
		output = invoke(append(slices.Clone(paired), "-no-gate"), 0, "")
		if !strings.Contains(output, "known key gaps (excluded from strict synonym lift)") || !strings.Contains(output, "| code-synonym | c.txt | candidate | 1.000000 | 1.000000 |") {
			t.Fatal(output)
		}
		data, err = os.ReadFile(prefix + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var gapResult report.Results
		if err := json.Unmarshal(data, &gapResult); err != nil {
			t.Fatal(err)
		}
		c := gapResult.Corpora["code"]
		if gapResult.Harness.QuerySHA256["synonym_or_key_gaps"] == "" || c.ByKind["synonym"] != nil || c.ByKind["known_key_gap"]["candidate"].R10 != 1 || c.Aggregate["candidate"].Unjudged10 != 0 || len(c.Queries[2].PerSystem["candidate"].GoldRanks) != 1 {
			t.Fatalf("gap separation: %+v", c)
		}
		if output := invoke(paired, 1, ""); !strings.Contains(output, "code synonym: missing paired metrics") {
			t.Fatal(output)
		}
		if err := os.Remove(gapFile); err != nil {
			t.Fatal(err)
		}
		invoke(paired, 2, "overlay.key_gaps.json")
	})
	t.Run("missing rg", func(t *testing.T) { t.Setenv("PATH", ""); invoke(args, 2, "rg") })
	invoke(append(slices.Clone(args), "-no-gate=false"), 2, "baseline missing: run make eval-baseline")
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
