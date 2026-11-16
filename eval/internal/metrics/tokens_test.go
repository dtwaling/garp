package metrics

import (
	"bytes"
	"os"
	"testing"
)

func TestCount(t *testing.T) {
	for _, tt := range []struct {
		text string
		want int
	}{
		{"", 0}, {"hello", 1}, {"hello world", 2},
	} {
		t.Run(tt.text, func(t *testing.T) {
			t.Parallel()
			if got := Count([]byte(tt.text)); got != tt.want {
				t.Errorf("Count(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestHitN(t *testing.T) {
	stdout, err := os.ReadFile("testdata/garp_json_hitn.json")
	if err != nil {
		t.Fatal(err)
	}
	ends, err := ResultEnds(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if len(ends) != 2 {
		t.Fatalf("ends = %v, want two results", ends)
	}
	// Locate the closing result braces independently, including the UTF-8 bytes.
	firstEnd := bytes.Index(stdout, []byte("\n    },")) + len("\n    }")
	lastEnd := bytes.Index(stdout, []byte("\n    }\n  ]")) + len("\n    }")
	if ends[0] != int64(firstEnd) || ends[1] != int64(lastEnd) {
		t.Fatalf("ends = %v, want [%d %d]", ends, firstEnd, lastEnd)
	}
	const budget = 118
	if first, last, full := Count(stdout[:firstEnd]), Count(stdout[:lastEnd]), Count(stdout); first != budget || last != 186 || full != 189 {
		t.Fatalf("fixture token counts = %d, %d, %d; want 118, 186, 189", first, last, full)
	}
	for _, tt := range []struct {
		name   string
		gold   []Gold
		budget int
		want   float64
	}{
		{"before budget", []Gold{{File: "early.go"}}, budget, 1},
		{"past budget", []Gold{{File: "late.pdf"}}, budget, 0},
		{"one token short", []Gold{{File: "early.go"}}, budget - 1, 0},
		{"zero budget", []Gold{{File: "early.go"}}, 0, 0},
		{"partial recall", []Gold{{File: "early.go"}, {File: "late.pdf"}}, budget, 0.5},
		{"all results", []Gold{{File: "early.go"}, {File: "late.pdf"}}, Count(stdout), 1},
		{"range start inclusive", []Gold{{File: "early.go", Lines: &[2]int{10, 12}}}, budget, 1},
		{"range end inclusive", []Gold{{File: "early.go", Lines: &[2]int{8, 10}}}, budget, 1},
		{"outside range", []Gold{{File: "early.go", Lines: &[2]int{11, 12}}}, budget, 0},
		{"ranges counted separately", []Gold{{File: "early.go", Lines: &[2]int{10, 12}}, {File: "early.go", Lines: &[2]int{20, 25}}}, budget, 0.5},
		{"no gold", nil, budget, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HitN(stdout, tt.gold, tt.budget)
			if err != nil || got != tt.want {
				t.Fatalf("HitN = %g, %v; want %g", got, err, tt.want)
			}
		})
	}
	// Evidence can come from any excerpt, but cannot inflate a gold item's hit.
	multi := []byte(`{"results":[{"file":"a","excerpts":[{"start_line":1},{"start_line":5},{"start_line":5}]},{"file":"a","excerpts":[{"start_line":5}]}]}`)
	if got, err := HitN(multi, []Gold{{File: "a", Lines: &[2]int{5, 5}}}, Count(multi)); err != nil || got != 1 {
		t.Fatalf("multiple excerpts/results: %g, %v; want 1", got, err)
	}
}

func TestResultEnds(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		want        int
		invalid     bool
	}{
		{"empty array", `{"results":[]}`, 0, false},
		{"null results", `{"results":null}`, 0, false},
		{"other fields", `{"before":{"results":[1]},"results":[{"file":"a"}],"after":[1,2]}`, 1, false},
		{"case distinct field", `{"results":[],"Results":[{"file":"absent"}]}`, 0, false},
		{"missing results", `{}`, 0, true},
		{"not object", `[]`, 0, true},
		{"not array", `{"results":{}}`, 0, true},
		{"truncated result", `{"results":[{"file":"a"}`, 0, true},
		{"truncated envelope", `{"results":[]`, 0, true},
		{"trailing data", `{"results":[]} {}`, 0, true},
		{"duplicate results", `{"results":[],"results":[]}`, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ends, err := ResultEnds([]byte(tt.input))
			if (err != nil) != tt.invalid || (!tt.invalid && len(ends) != tt.want) {
				t.Fatalf("ResultEnds = %v, %v; want length %d, invalid %v", ends, err, tt.want, tt.invalid)
			}
			got, err := HitN([]byte(tt.input), []Gold{{File: "absent"}}, 2000)
			if (err != nil) != tt.invalid || got != 0 {
				t.Fatalf("HitN = %g, %v", got, err)
			}
		})
	}
}
