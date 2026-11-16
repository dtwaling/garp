// Package report writes machine-independent metrics and informational markdown.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"garp/eval/internal/metrics"
)

// Number retains full precision for comparisons; only JSON is rounded.
type Number float64

func (n Number) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("non-finite metric")
	}
	return []byte(strconv.FormatFloat(math.Round(f*1e6)/1e6, 'f', -1, 64)), nil
}

type Results struct {
	Schema  int               `json:"schema"`
	Harness Harness           `json:"harness"`
	Systems Systems           `json:"systems"`
	Corpora map[string]Corpus `json:"corpora"`
}
type Harness struct {
	QuerySHA256    map[string]string `json:"query_sha256"`
	ManifestSHA256 map[string]string `json:"manifest_sha256"`
	Tokenizer      string            `json:"tokenizer"`
	TokenBudget    map[string]int    `json:"token_budget"`
	RGVersion      string            `json:"rg_version"`
}
type Version struct {
	Version string `json:"version"`
}
type Previous struct {
	Version string `json:"version"`
	Ref     string `json:"ref"`
}
type BM25 struct {
	K1 Number `json:"k1"`
	B  Number `json:"b"`
}
type Systems struct {
	Candidate Version  `json:"candidate"`
	Previous  Previous `json:"previous"`
	BM25      BM25     `json:"bm25"`
	RG        struct{} `json:"rg"`
}
type Summary struct {
	R1         Number  `json:"r1"`
	R5         Number  `json:"r5"`
	R10        Number  `json:"r10"`
	MRR10      Number  `json:"mrr10"`
	HitN       *Number `json:"hit_n"`
	TokensMean *Number `json:"tokens_mean"`
	TokensP50  *Number `json:"tokens_p50"`
	Unjudged10 Number  `json:"unjudged10"`
}
type Corpus struct {
	Aggregate map[string]Summary            `json:"aggregate"`
	ByKind    map[string]map[string]Summary `json:"by_kind"`
	Queries   []Query                       `json:"queries"`
	Files     int                           `json:"-"`
	Timing    map[string][]time.Duration    `json:"-"`
	IndexWall time.Duration                 `json:"-"`
}
type Query struct {
	ID        string                  `json:"id"`
	Kind      string                  `json:"kind"`
	PerSystem map[string]QueryMetrics `json:"per_system"`
}

// GoldRanks has one rank per distinct gold file, in gold-file order; zero
// means absent. Unjudged paths are retained only for the markdown judgment list.
type QueryMetrics struct {
	GoldRanks  []int    `json:"gold_ranks"`
	Returned   int      `json:"returned"`
	Tokens     *int     `json:"tokens"`
	HitN       *Number  `json:"hit_n"`
	Unjudged10 int      `json:"unjudged10"`
	Error      string   `json:"error"`
	Unjudged   []string `json:"-"`
}

// Summarize computes macro means without rounding; n/a values stay null.
func Summarize(queries []Query, system string) Summary {
	var r1, r5, r10, mrr, hit, tokens, unjudged []float64
	for _, q := range queries {
		m := q.PerSystem[system]
		recall := func(k int) float64 {
			n := 0
			for _, rank := range m.GoldRanks {
				if rank > 0 && rank <= k {
					n++
				}
			}
			if len(m.GoldRanks) == 0 {
				return 0
			}
			return float64(n) / float64(len(m.GoldRanks))
		}
		r1 = append(r1, recall(1))
		r5 = append(r5, recall(5))
		r10 = append(r10, recall(10))
		rr := 0.0
		if rank := firstGold(m.GoldRanks); rank > 0 && rank <= 10 {
			rr = 1 / float64(rank)
		}
		mrr = append(mrr, rr)
		unjudged = append(unjudged, float64(m.Unjudged10))
		if m.HitN != nil {
			hit = append(hit, float64(*m.HitN))
		}
		if m.Tokens != nil {
			tokens = append(tokens, float64(*m.Tokens))
		}
	}
	s := Summary{R1: Number(metrics.Mean(r1)), R5: Number(metrics.Mean(r5)), R10: Number(metrics.Mean(r10)), MRR10: Number(metrics.Mean(mrr)), Unjudged10: Number(metrics.Mean(unjudged))}
	if len(hit) > 0 {
		v := Number(metrics.Mean(hit))
		s.HitN = &v
	}
	if len(tokens) > 0 {
		mean, p50 := Number(metrics.Mean(tokens)), Number(percentile(tokens, 0.5))
		s.TokensMean = &mean
		s.TokensP50 = &p50
	}
	return s
}

func WriteJSON(w io.Writer, r Results) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

var systemOrder = []string{"candidate", "previous", "bm25", "rg"}

// WriteMarkdown keeps timing and unjudged paths separate from the deterministic
// JSON. baseline, when supplied, is used only for first-gold rank changes.
func WriteMarkdown(w io.Writer, r Results, baseline *Results) error {
	var b strings.Builder
	for _, name := range []string{"docs", "code"} {
		c, ok := r.Corpora[name]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "## %s (%d queries, %d files) -- token budget N = %d (o200k_base)\n\n", name, len(c.Queries), c.Files, r.Harness.TokenBudget[name])
		b.WriteString("| system | R@1 | R@5 | R@10 | MRR@10 | hit@N | tokens mean / p50 | unjudged@10 |\n|---|---|---|---|---|---|---|---|\n")
		for _, system := range systemOrder {
			s, ok := c.Aggregate[system]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "| %s | %.6f | %.6f | %.6f | %.6f | %s | %s / %s | %.6f |\n", system, s.R1, s.R5, s.R10, s.MRR10, optional(s.HitN), optional(s.TokensMean), optional(s.TokensP50), s.Unjudged10)
		}
		b.WriteString("\n### by kind (R@5 / MRR@10)\n\n| system | proximity | synonym | single |\n|---|---|---|---|\n")
		for _, system := range systemOrder {
			fmt.Fprintf(&b, "| %s |", system)
			for _, kind := range []string{"proximity", "synonym", "single"} {
				if s, ok := c.ByKind[kind][system]; ok {
					fmt.Fprintf(&b, " %.6f / %.6f |", s.R5, s.MRR10)
				} else {
					b.WriteString(" n/a |")
				}
			}
			b.WriteByte('\n')
		}
		if name == "docs" {
			fmt.Fprintf(&b, "\n### decision gate (docs): garp MRR@10 - bm25 MRR@10 = %+.6f\n", c.Aggregate["candidate"].MRR10-c.Aggregate["bm25"].MRR10)
		}
		if baseline != nil {
			b.WriteString("\n### per-query changes vs baseline\n")
			old := make(map[string]QueryMetrics)
			for _, q := range baseline.Corpora[name].Queries {
				old[q.ID] = q.PerSystem["candidate"]
			}
			changed := false
			for _, q := range c.Queries {
				if prev, ok := old[q.ID]; ok {
					before, after := firstGold(prev.GoldRanks), firstGold(q.PerSystem["candidate"].GoldRanks)
					if before != after {
						fmt.Fprintf(&b, "- %s: first-gold rank %d -> %d (0 = absent)\n", q.ID, before, after)
						changed = true
					}
				}
			}
			if !changed {
				b.WriteString("none\n")
			}
		}
		b.WriteString("\n### unjudged top-10 files\n")
		anyUnjudged := false
		for _, q := range c.Queries {
			for _, system := range systemOrder {
				if files := q.PerSystem[system].Unjudged; len(files) > 0 {
					fmt.Fprintf(&b, "- %s %s: %s\n", q.ID, system, strings.Join(files, ", "))
					anyUnjudged = true
				}
			}
		}
		if !anyUnjudged {
			b.WriteString("none\n")
		}
		fmt.Fprintf(&b, "\n### timing (informational, machine-dependent; one pass)\n\nBM25 index build: %.6f s\n\n| system | p50 (s) | p95 (s) | total (s) |\n|---|---|---|---|\n", c.IndexWall.Seconds())
		for _, system := range systemOrder {
			var seconds []float64
			total := 0.0
			for _, d := range c.Timing[system] {
				seconds = append(seconds, d.Seconds())
				total += d.Seconds()
			}
			fmt.Fprintf(&b, "| %s | %.6f | %.6f | %.6f |\n", system, percentile(seconds, 0.5), percentile(seconds, 0.95), total)
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}
func optional(v *Number) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.6f", *v)
}
func firstGold(ranks []int) int {
	first := 0
	for _, r := range ranks {
		if r > 0 && (first == 0 || r < first) {
			first = r
		}
	}
	return first
}

// Percentiles use nearest rank (ceil(p*n)), including p50 for even samples.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	return v[int(math.Ceil(p*float64(len(v))))-1]
}
