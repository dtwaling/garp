package metrics

import (
	"reflect"
	"testing"
)

func TestRetrievalMetrics(t *testing.T) {
	for _, tt := range []struct {
		name             string
		gold             map[string]bool
		ranking          []string
		r1, r5, r10, mrr float64
	}{
		{"gold at rank three", map[string]bool{"gold": true}, []string{"a", "b", "gold"}, 0, 1, 1, 1.0 / 3},
		{"one of two gold", map[string]bool{"gold": true, "other": true}, []string{"gold", "a", "b", "c", "d", "other"}, 0.5, 0.5, 1, 1},
		{"gold at rank eleven", map[string]bool{"gold": true}, []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "gold"}, 0, 0, 0, 0},
		{"duplicate results keep first rank", map[string]bool{"gold": true}, []string{"a", "a", "gold", "gold"}, 0, 1, 1, 0.5},
		{"no results", map[string]bool{"gold": true}, nil, 0, 0, 0, 0},
		{"empty gold", nil, []string{"a"}, 0, 0, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for k, want := range map[int]float64{-1: 0, 0: 0, 1: tt.r1, 5: tt.r5, 10: tt.r10} {
				if got := RecallAt(tt.gold, tt.ranking, k); got != want {
					t.Errorf("RecallAt(k=%d) = %g, want %g", k, got, want)
				}
			}
			if got := MRRAt10(tt.gold, tt.ranking); got != tt.mrr {
				t.Errorf("MRRAt10 = %g, want %g", got, tt.mrr)
			}
		})
	}
}

func TestUnjudged10(t *testing.T) {
	ranking := []string{"gold", "gold", "judged", "a", "b", "c", "d", "e", "f", "g", "h", "outside"}
	want := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	if got := Unjudged10(map[string]bool{"gold": true}, map[string]bool{"judged": true}, ranking); !reflect.DeepEqual(got, want) {
		t.Fatalf("Unjudged10 = %v, want %v", got, want)
	}
	if got := Unjudged10(nil, nil, nil); len(got) != 0 {
		t.Fatalf("Unjudged10(empty) = %v", got)
	}
}

func TestMean(t *testing.T) {
	for _, tt := range []struct {
		values []float64
		want   float64
	}{
		{nil, 0}, {[]float64{0.5}, 0.5}, {[]float64{0, 0.5, 1}, 0.5},
	} {
		if got := Mean(tt.values); got != tt.want {
			t.Errorf("Mean(%v) = %g, want %g", tt.values, got, tt.want)
		}
	}
}
