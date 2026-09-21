package search

import "testing"

func TestSmartFormsDiscovery(t *testing.T) {
	t.Run("smart forms enabled", func(t *testing.T) {
		t.Setenv("GARP_SMART_FORMS", "1")

		for _, tt := range []struct {
			name string
			buf  string
			word string
			mode PartialMode
			want bool
		}{
			{name: "ed suffix", buf: "deployed", word: "deploy", mode: PartialModeOff, want: true},
			{name: "ing suffix", buf: "deploying", word: "deploy", mode: PartialModeOff, want: true},
			{name: "al suffix", buf: "deployal", word: "deploy", mode: PartialModeOff, want: true},
			{name: "tion suffix", buf: "deploytion", word: "deploy", mode: PartialModeOff, want: true},
			{name: "ation suffix", buf: "deployation", word: "deploy", mode: PartialModeOff, want: true},
			{name: "s suffix remains accepted", buf: "deploys", word: "deploy", mode: PartialModeOff, want: true},
			{name: "es suffix remains accepted", buf: "deployes", word: "deploy", mode: PartialModeOff, want: true},
			{name: "left boundary remains enforced", buf: "redeploy", word: "deploy", mode: PartialModeOff, want: false},
			{name: "prefix mode remains unaffected", buf: "deployment", word: "deploy", mode: PartialModePrefix, want: true},
			{name: "contains mode remains unaffected", buf: "deployment", word: "deploy", mode: PartialModeContains, want: true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				got := asciiIndexPartialCI([]byte(tt.buf), []byte(tt.word), tt.mode) >= 0
				if got != tt.want {
					t.Errorf("asciiIndexPartialCI(%q, %q, %q) found = %t, want %t", tt.buf, tt.word, tt.mode, got, tt.want)
				}
			})
		}
	})

	t.Run("smart forms disabled preserves legacy suffixes", func(t *testing.T) {
		t.Setenv("GARP_SMART_FORMS", "")
		if got := asciiIndexPartialCI([]byte("deployed"), []byte("deploy"), PartialModeOff) >= 0; got {
			t.Error("asciiIndexPartialCI found deployed with smart forms disabled, want false")
		}
	})
}
