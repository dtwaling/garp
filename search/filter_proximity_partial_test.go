package search_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestCheckTextContainsAllWordsPartial(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		words    []string
		distance int
		partial  search.PartialMode
		want     bool
	}{
		{name: "prefix uses captured token start for distance", text: "_deployment service", words: []string{"deploy", "service"}, distance: 11, partial: search.PartialModePrefix, want: true},
		{name: "off rejects derivative", text: "_deployment service", words: []string{"deploy", "service"}, distance: 50, partial: search.PartialModeOff, want: false},
		{name: "prefix accepts derivative", text: "_deployment service", words: []string{"deploy", "service"}, distance: 50, partial: search.PartialModePrefix, want: true},
		{name: "contains accepts derivative", text: "_deployment service", words: []string{"deploy", "service"}, distance: 50, partial: search.PartialModeContains, want: true},
		{name: "inline glob expands while off", text: "deployment service", words: []string{"deploy*", "service"}, distance: 50, partial: search.PartialModeOff, want: true},
		{name: "prefix rejects internal substring", text: "redeploy service", words: []string{"deploy", "service"}, distance: 50, partial: search.PartialModePrefix, want: false},
		{name: "contains accepts internal substring", text: "redeploy service", words: []string{"deploy", "service"}, distance: 50, partial: search.PartialModeContains, want: true},
		{name: "distance remains bounded", text: "deploying " + strings.Repeat("x", 300) + " service", words: []string{"deploy", "service"}, distance: 100, partial: search.PartialModePrefix, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := search.CheckTextContainsAllWordsPartial(tt.text, tt.words, tt.distance, tt.partial); got != tt.want {
				t.Fatalf("CheckTextContainsAllWordsPartial() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPartialStreamingPrefilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.eml")
	if err := os.WriteFile(path, []byte("_deployment service"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	for _, tt := range []struct {
		name    string
		words   []string
		partial search.PartialMode
		want    bool
	}{
		{name: "off rejects derivative", words: []string{"deploy", "service"}, partial: search.PartialModeOff, want: false},
		{name: "prefix accepts derivative", words: []string{"deploy", "service"}, partial: search.PartialModePrefix, want: true},
		{name: "glob expands while off", words: []string{"deploy*", "service"}, partial: search.PartialModeOff, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			found, decided := search.BinaryStreamingPrefilterDecidedPartial(path, tt.words, 0, tt.partial)
			if !decided {
				t.Fatal("prefilter was inconclusive for a small candidate")
			}
			if found != tt.want {
				t.Fatalf("BinaryStreamingPrefilterDecidedPartial() = %v, want %v", found, tt.want)
			}
		})
	}
}

func TestSearchEngineThreadsPartialModeThroughDiscoveryAndFiltering(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "candidate.txt"), []byte("_deployment service"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	for _, tt := range []struct {
		name    string
		partial search.PartialMode
		want    int
	}{
		{name: "off rejects derivative at discovery", partial: search.PartialModeOff, want: 0},
		{name: "prefix retains derivative through stage two", partial: search.PartialModePrefix, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			engine := search.NewSearchEngineWithWorkers([]string{"deploy", "service"}, nil, []string{"-g", "*.txt"}, false, 1, 1000, 1)
			engine.StartDir = root
			engine.Distance = 50
			engine.Partial = tt.partial
			engine.Silent = true

			results, err := engine.Execute()
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if len(results) != tt.want {
				t.Fatalf("result count = %d, want %d", len(results), tt.want)
			}
		})
	}
}
