package report

import (
	"fmt"
	"reflect"
	"testing"
)

func gateResults() Results {
	r := Results{Schema: 1, Harness: Harness{Tokenizer: "o200k_base tiktoken-go/tokenizer v0.10.0", QuerySHA256: map[string]string{}, ManifestSHA256: map[string]string{}}, Corpora: map[string]Corpus{}}
	r.Systems.Previous = Previous{Ref: "v0.10.0", Version: "garp v0.10.0"}
	for _, name := range []string{"docs", "code"} {
		r.Harness.QuerySHA256[name] = "query-" + name
		r.Harness.ManifestSHA256[name] = "manifest-" + name
		hit := Number(0.5)
		r.Corpora[name] = Corpus{Aggregate: map[string]Summary{"candidate": {R1: 0.5, R5: 0.5, R10: 0.5, MRR10: 0.5, HitN: &hit}}}
	}
	return r
}

func TestGate(t *testing.T) {
	for _, corpus := range []string{"docs", "code"} {
		for _, metric := range []string{"r1", "r5", "r10", "mrr10", "hit_n"} {
			for _, decrease := range []Number{0.01, 0, 1e-10, 2e-9, -0.01} {
				baseline, current := gateResults(), gateResults()
				s := baseline.Corpora[corpus].Aggregate["candidate"]
				fields := map[string]*Number{"r1": &s.R1, "r5": &s.R5, "r10": &s.R10, "mrr10": &s.MRR10, "hit_n": s.HitN}
				*fields[metric] += decrease
				baseline.Corpora[corpus].Aggregate["candidate"] = s
				var want []Regression
				if decrease > 1e-9 {
					want = []Regression{{Corpus: corpus, Metric: metric, Baseline: 0.5 + decrease, Candidate: 0.5}}
				}
				if got := Gate(baseline, current); !reflect.DeepEqual(got, want) {
					t.Errorf("%s %s decrease=%g: got %+v want %+v", corpus, metric, decrease, got, want)
				}
			}
		}
	}
	baseline, current := gateResults(), gateResults()
	for _, corpus := range []string{"docs", "code"} {
		s := baseline.Corpora[corpus].Aggregate["candidate"]
		v := Number(100)
		s.TokensMean, s.TokensP50, s.Unjudged10 = &v, &v, v
		baseline.Corpora[corpus].Aggregate["candidate"] = s
		for _, system := range []string{"previous", "bm25", "rg"} {
			baseline.Corpora[corpus].Aggregate[system] = Summary{R1: 1, R5: 1, R10: 1, MRR10: 1, HitN: &v}
		}
	}
	if got := Gate(baseline, current); len(got) != 0 {
		t.Fatalf("informational metrics gated: %+v", got)
	}
}

func TestStale(t *testing.T) {
	if err := Stale(gateResults(), gateResults()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tokenizer string
		schema          int
	}{
		{"tokenizer mismatch", "other-tokenizer", 1},
		{"empty tokenizer", "", 1},
		{"schema mismatch", gateResults().Harness.Tokenizer, 2},
		{"zero schema", gateResults().Harness.Tokenizer, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseline, current := gateResults(), gateResults()
			baseline.Harness.Tokenizer, baseline.Schema = tc.tokenizer, tc.schema
			want := fmt.Sprintf("baseline stale: recorded for tokenizer %q, run uses tokenizer %q; run make eval-baseline", tc.tokenizer, current.Harness.Tokenizer)
			if tc.schema != current.Schema {
				want = fmt.Sprintf("baseline stale: recorded for schema %d, run uses schema %d; run make eval-baseline", tc.schema, current.Schema)
			}
			if err := Stale(baseline, current); err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
	for _, tc := range []struct {
		name, ref, version string
		stale              bool
	}{
		{"matching release", "v0.10.0", "garp v0.10.0", false},
		{"ref mismatch", "v0.9.0", "garp v0.10.0", true},
		{"version mismatch", "v0.10.0", "garp v0.9.0", true},
		{"empty ref", "", "garp v0.10.0", true},
		{"empty version", "v0.10.0", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseline, current := gateResults(), gateResults()
			baseline.Systems.Previous = Previous{Ref: tc.ref, Version: tc.version}
			if err := Stale(baseline, current); (err != nil) != tc.stale {
				t.Fatalf("stale=%v: %v", tc.stale, err)
			}
		})
	}
	for _, corpus := range []string{"docs", "code"} {
		for _, kind := range []string{"query", "manifest"} {
			for _, value := range []string{"changed", ""} {
				baseline, current := gateResults(), gateResults()
				hashes := baseline.Harness.QuerySHA256
				if kind == "manifest" {
					hashes = baseline.Harness.ManifestSHA256
				}
				hashes[corpus] = value
				if err := Stale(baseline, current); err == nil || err.Error() != "baseline stale: run make eval-baseline" {
					t.Errorf("%s %s %q: %v", corpus, kind, value, err)
				}
			}
		}
	}
}
