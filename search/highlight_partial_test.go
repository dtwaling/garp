package search

import "testing"

func TestHighlightTermsPartial(t *testing.T) {
	const hi = "\033[1;31m"
	const nc = "\033[0m"

	tests := []struct {
		name  string
		text  string
		terms []string
		mode  PartialMode
		want  string
	}{
		{
			name:  "prefix preserves underscore boundary",
			text:  "Production _deployment active",
			terms: []string{"deploy"},
			mode:  PartialModePrefix,
			want:  "Production _" + hi + "deployment" + nc + " active",
		},
		{
			name:  "glob expands prefix while off",
			text:  "Production deploy* active",
			terms: []string{"deploy*"},
			mode:  PartialModeOff,
			want:  "Production " + hi + "deploy" + nc + "* active",
		},
		{
			name:  "contains highlights internal substring",
			text:  "redeploy now",
			terms: []string{"deploy"},
			mode:  PartialModeContains,
			want:  "re" + hi + "deploy" + nc + " now",
		},
		{
			name:  "prefix rejects internal substring",
			text:  "redeploy now",
			terms: []string{"deploy"},
			mode:  PartialModePrefix,
			want:  "redeploy now",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HighlightTermsPartial(tt.text, tt.terms, tt.mode); got != tt.want {
				t.Errorf("HighlightTermsPartial() = %q, want %q", got, tt.want)
			}
		})
	}
}
