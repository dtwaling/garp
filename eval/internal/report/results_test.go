package report

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	hit, zero := Number(1), Number(0)
	a, b := 10, 20
	queries := []Query{
		{PerSystem: map[string]QueryMetrics{"candidate": {GoldRanks: []int{3, 0}, Tokens: &a, HitN: &hit, Unjudged10: 2}}},
		{PerSystem: map[string]QueryMetrics{"candidate": {GoldRanks: []int{1}, Tokens: &b, HitN: &zero}}},
	}
	s := Summarize(queries, "candidate")
	if s.R1 != 0.5 || s.R5 != 0.75 || s.R10 != 0.75 || math.Abs(float64(s.MRR10)-2.0/3) > 1e-12 || *s.HitN != 0.5 || *s.TokensMean != 15 || *s.TokensP50 != 10 || s.Unjudged10 != 1 {
		t.Fatalf("macro metrics: %+v", s)
	}
	if s := Summarize(queries, "bm25"); s.HitN != nil || s.TokensMean != nil || s.TokensP50 != nil {
		t.Fatalf("n/a metrics: %+v", s)
	}
}

func TestResultsWriters(t *testing.T) {
	r := Results{Schema: 1, Harness: Harness{TokenBudget: map[string]int{"docs": 2000}}, Corpora: map[string]Corpus{"docs": {
		Files: 3, Aggregate: map[string]Summary{"candidate": {MRR10: 1.0 / 3}, "bm25": {MRR10: 0.25}},
		ByKind:  map[string]map[string]Summary{"single": {"candidate": {R5: 1, MRR10: 1.0 / 3}}},
		Queries: []Query{{ID: "q", Kind: "single", PerSystem: map[string]QueryMetrics{"candidate": {GoldRanks: []int{3}, Unjudged: []string{"other.txt"}}}}},
		Timing:  map[string][]time.Duration{"candidate": {time.Second, 2 * time.Second}},
	}}}
	var a, b bytes.Buffer
	if err := WriteJSON(&a, r); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&b, r); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) || !strings.Contains(a.String(), `"mrr10": 0.333333`) {
		t.Fatalf("unstable/unrounded JSON: %s", a.String())
	}
	if strings.Contains(a.String(), "timing") || strings.Contains(a.String(), "other.txt") {
		t.Fatal("markdown-only data leaked")
	}
	oldQuery := Query{ID: "q", PerSystem: map[string]QueryMetrics{"candidate": {GoldRanks: []int{1}}}}
	baseline := Results{Corpora: map[string]Corpus{"docs": {Queries: []Query{oldQuery}}}}
	a.Reset()
	if err := WriteMarkdown(&a, r, &baseline); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## docs (1 queries, 3 files)", "| candidate | 0.000000 | 0.000000 | 0.000000 | 0.333333 |", "### by kind (R@5 / MRR@10)", "### decision gate (docs): garp MRR@10 - bm25 MRR@10 = +0.083333", "q: first-gold rank 1 -> 3", "q candidate: other.txt", "one pass", "| candidate | 1.000000 | 2.000000 | 3.000000 |"} {
		if !strings.Contains(a.String(), want) {
			t.Errorf("missing %q in:\n%s", want, a.String())
		}
	}
}
