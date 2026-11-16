package queryset

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	paths := []string{"garp/search/ignore.go", "garp/README.md", "govdocs/x.pdf"}
	for _, tc := range []struct {
		name   string
		change func(*Set, *Minimums)
		want   string
	}{
		{"valid", func(*Set, *Minimums) {}, ""},
		{"unknown flag", func(s *Set, _ *Minimums) { s.Queries[0].Args = []string{"--not"} }, "args"},
		{"missing gold", func(s *Set, _ *Minimums) { s.Queries[0].Gold[0].File = "missing.go" }, "manifest"},
		{"missing nonrelevant", func(s *Set, _ *Minimums) { s.Queries[0].JudgedNonrelevant = []string{"missing.go"} }, "manifest"},
		{"binary lines", func(s *Set, _ *Minimums) {
			s.Queries[0].Root = "."
			s.Queries[0].Gold[0].File = "govdocs/x.pdf"
			s.Queries[0].JudgedNonrelevant = nil
		}, "binary"},
		{"zero line", func(s *Set, _ *Minimums) { s.Queries[0].Gold[0].Lines = []int{0, 1} }, "lines"},
		{"reversed lines", func(s *Set, _ *Minimums) { s.Queries[0].Gold[0].Lines = []int{3, 2} }, "lines"},
		{"extra line", func(s *Set, _ *Minimums) { s.Queries[0].Gold[0].Lines = []int{1, 2, 3} }, "lines"},
		{"empty lines", func(s *Set, _ *Minimums) { s.Queries[0].Gold[0].Lines = []int{} }, "lines"},
		{"query minimum", func(_ *Set, m *Minimums) { m.Queries = 2 }, "queries"},
		{"proximity minimum", func(_ *Set, m *Minimums) { m.Proximity = 2 }, "proximity"},
		{"synonym minimum", func(_ *Set, m *Minimums) { m.Synonym = 1 }, "synonym"},
		{"single minimum", func(_ *Set, m *Minimums) { m.Single = 1 }, "single"},
		{"garp minimum", func(_ *Set, m *Minimums) { m.Garp = 2 }, "garp"},
		{"ripgrep minimum", func(_ *Set, m *Minimums) { m.Ripgrep = 1 }, "ripgrep"},
		{"format minimum", func(s *Set, m *Minimums) { s.Corpus = "docs"; m.DocsPerFormat = 1 }, "pdf"},
		{"duplicate ids", func(s *Set, _ *Minimums) { s.Queries = append(s.Queries, s.Queries[0]) }, "duplicate"},
		{"budget", func(s *Set, _ *Minimums) { s.TokenBudget = 0 }, "token_budget"},
		{"escape root", func(s *Set, _ *Minimums) { s.Queries[0].Root = "../garp" }, "root"},
		{"escape file", func(s *Set, _ *Minimums) { s.Queries[0].Gold[0].File = "../garp/search/ignore.go" }, "path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Set{Corpus: "code", TokenBudget: 2000, Queries: []Query{{ID: "query-a", Root: "garp", Kind: "proximity", Need: "Find nested ignore rules", Args: []string{"gitignore", "nested", "--distance", "300"}, Gold: []Gold{{File: "search/ignore.go", Lines: []int{1, 2}}}, JudgedNonrelevant: []string{"README.md"}}}}
			m := Minimums{Queries: 1, Proximity: 1}
			tc.change(&s, &m)
			errs := Validate(s, paths, m)
			if tc.want == "" {
				if len(errs) != 0 {
					t.Fatal(errs)
				}
				return
			}
			found := false
			for _, err := range errs {
				if !strings.Contains(err.Error(), "query-a") {
					t.Errorf("missing id: %v", err)
				}
				found = found || strings.Contains(err.Error(), tc.want)
			}
			if !found {
				t.Fatalf("want %q, got %v", tc.want, errs)
			}
		})
	}
}

func TestArgs(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		valid bool
	}{
		{[]string{"word", "snake_case", "a.b", "x-y", "prefix*", "--distance", "300", "--strict", "--partial", "--partial=prefix", "--partial=contains", "--smart-forms", "-m", "2"}, true},
		{[]string{"--json"}, false}, {[]string{"--startdir", "."}, false},
		{[]string{"--no-ignore"}, false}, {[]string{"--file-timeout-binary", "60000"}, false},
		{[]string{"--code"}, false}, {[]string{"--max-results", "0"}, false},
		{[]string{"--json=true"}, false}, {[]string{"--distance"}, false},
		{[]string{"--distance", "0"}, false}, {[]string{"-m", "-1"}, false},
		{[]string{"-m", "many"}, false}, {[]string{"--partial=wrong"}, false},
		{[]string{"a*b"}, false}, {[]string{"a**"}, false}, {[]string{"a|b"}, false},
		{[]string{"two words"}, false}, {[]string{"--"}, false}, {[]string{"-unknown"}, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			if err := validateArgs(tc.args); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestDesignMinimums(t *testing.T) {
	m := DesignMinimums()
	if m != (Minimums{Queries: 40, Proximity: 12, Synonym: 10, Single: 10, DocsPerFormat: 4, Garp: 15, Ripgrep: 20}) {
		t.Fatal(m)
	}
	var s Set
	s.Corpus, s.TokenBudget = "docs", 2000
	var paths []string
	for _, ext := range []string{"pdf", "doc", "docx", "rtf", "eml", "msg"} {
		paths = append(paths, "x."+ext)
	}
	for i := 0; i < 40; i++ {
		kind := "single"
		if i < 12 {
			kind = "proximity"
		} else if i < 22 {
			kind = "synonym"
		}
		q := Query{ID: fmt.Sprintf("q-%d", i), Root: ".", Kind: kind, Args: []string{"word"}}
		for _, p := range paths {
			q.Gold = append(q.Gold, Gold{File: p})
		}
		s.Queries = append(s.Queries, q)
	}
	if errs := Validate(s, paths, m); len(errs) != 0 {
		t.Fatal(errs)
	}
	// Multiple items of the same format in one query count only once.
	for i := range s.Queries {
		if i >= 3 {
			s.Queries[i].Gold = s.Queries[i].Gold[1:]
		} else {
			s.Queries[i].Gold = append(s.Queries[i].Gold, Gold{File: "x.pdf"})
		}
	}
	if errs := Validate(s, paths, m); len(errs) != 1 || !strings.Contains(errs[0].Error(), "pdf") {
		t.Fatal(errs)
	}
	s.Corpus = "code"
	paths = []string{"garp/x.go", "ripgrep/x.rs"}
	for i := range s.Queries {
		q := &s.Queries[i]
		q.Root, q.Gold = "ripgrep", []Gold{{File: "x.rs"}}
		if i < 15 {
			q.Root, q.Gold = "garp", []Gold{{File: "x.go"}}
		}
	}
	if errs := Validate(s, paths, m); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "queries.json")
	for _, raw := range []string{`{"corpus":"code","token_budget":2000,"queries":[{"id":"q","root":"garp","kind":"single","need":"Find ignore","args":["ignore"],"gold":[{"file":"ignore.go","lines":[1,2]}],"judged_nonrelevant":[]}]}`, `{"corpus":`, `{ } { }`} {
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		s, hash, err := Load(p)
		if strings.Contains(raw, "token_budget") {
			if err != nil || s.Queries[0].Gold[0].Lines[1] != 2 || hash != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) {
				t.Fatalf("%+v %s %v", s, hash, err)
			}
		} else if err == nil {
			t.Fatal("accepted invalid JSON")
		}
	}
	if _, _, err := Load(p + "missing"); err == nil {
		t.Fatal("accepted missing file")
	}
}
