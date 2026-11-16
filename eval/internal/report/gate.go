package report

import "fmt"

// Stale refuses comparisons unless both corpora have matching query and
// manifest hashes. Missing hashes cannot establish a comparable baseline.
func Stale(baseline, current Results) error {
	for _, name := range []string{"docs", "code"} {
		for _, pair := range [][2]string{
			{baseline.Harness.QuerySHA256[name], current.Harness.QuerySHA256[name]},
			{baseline.Harness.ManifestSHA256[name], current.Harness.ManifestSHA256[name]},
		} {
			if pair[0] == "" || pair[0] != pair[1] {
				return fmt.Errorf("baseline stale: run make eval-baseline")
			}
		}
	}
	return nil
}

type Regression struct {
	Corpus, Metric      string
	Baseline, Candidate Number
}

// Gate compares only candidate retrieval aggregates, in corpus/metric order.
// Both inputs must contain complete candidate summaries (including hit_n).
// Callers must use the serialized Results representation for both inputs:
// comparing a rounded baseline to unrounded measurements can invent a decrease.
func Gate(baseline, current Results) []Regression {
	var regressions []Regression
	for _, name := range []string{"docs", "code"} {
		before := baseline.Corpora[name].Aggregate["candidate"]
		after := current.Corpora[name].Aggregate["candidate"]
		for _, m := range []struct {
			name          string
			before, after Number
		}{
			{"r1", before.R1, after.R1},
			{"r5", before.R5, after.R5},
			{"r10", before.R10, after.R10},
			{"mrr10", before.MRR10, after.MRR10},
			{"hit_n", *before.HitN, *after.HitN},
		} {
			if m.after < m.before-1e-9 {
				regressions = append(regressions, Regression{Corpus: name, Metric: m.name, Baseline: m.before, Candidate: m.after})
			}
		}
	}
	return regressions
}
