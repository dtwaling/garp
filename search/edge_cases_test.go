package search_test

import (
	"strings"
	"testing"

	"garp/search"
)

func TestPartialEdgeCases(t *testing.T) {
	t.Run("partial and glob matches contribute distinct ranked terms", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			text    string
			words   []string
			partial search.PartialMode
		}{
			{
				name:    "prefix",
				text:    "deployment configuration",
				words:   []string{"deploy", "config"},
				partial: search.PartialModePrefix,
			},
			{
				name:    "inline glob",
				text:    "deployment config",
				words:   []string{"deploy*", "config"},
				partial: search.PartialModeOff,
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				score, count, terms, _, ok := search.CheckTextContainsRankedWordsPartial(tt.text, tt.words, 50, true, tt.partial)
				if !ok {
					t.Fatal("expected a strict ranked match")
				}
				if score != len(tt.words[0])+len(tt.words[1]) || count != 2 {
					t.Fatalf("ranked match = score %d, term count %d; want score %d and term count 2", score, count, len(tt.words[0])+len(tt.words[1]))
				}
				if strings.Join(terms, ",") != strings.Join(tt.words, ",") {
					t.Fatalf("matched terms = %v, want %v", terms, tt.words)
				}
			})
		}
	})

	t.Run("quoted phrase expands only its terminal word", func(t *testing.T) {
		score, count, terms, _, ok := search.CheckTextContainsRankedWordsPartial("deploy services", []string{"deploy service"}, 50, true, search.PartialModePrefix)
		if !ok || score != len("deploy service") || count != 1 || strings.Join(terms, ",") != "deploy service" {
			t.Fatalf("terminal-word phrase match = (%d, %d, %v, %v), want (%d, 1, [deploy service], true)", score, count, terms, ok, len("deploy service"))
		}
		if _, _, _, _, ok := search.CheckTextContainsRankedWordsPartial("deployment service", []string{"deploy service"}, 50, true, search.PartialModePrefix); ok {
			t.Fatal("prefix mode expanded the first word of a quoted phrase")
		}
	})

	t.Run("identifier boundaries preserve the token start", func(t *testing.T) {
		for _, text := range []string{"deploy_endpoint", "webdash-deploy"} {
			idx := search.FindTermIndexCI(text, "deploy", search.PartialModePrefix)
			if len(idx) != 2 || idx[0] < 0 || text[idx[0]:idx[1]] != "deploy_endpoint" && text[idx[0]:idx[1]] != "deploy" {
				t.Fatalf("FindTermIndexCI(%q) = %v, expected token-only deploy match", text, idx)
			}
			if text[idx[0]] != 'd' {
				t.Fatalf("FindTermIndexCI(%q) starts at %d (%q), want deploy token start", text, idx[0], text[idx[0]])
			}
		}
		if idx := search.FindTermIndexCI("autoDeploy", "deploy", search.PartialModePrefix); idx != nil {
			t.Fatalf("camelCase transition unexpectedly matched under RE2 prefix mode: %v", idx)
		}
	})

	t.Run("exclusions remain whole word", func(t *testing.T) {
		for _, text := range []string{"latest changes", "attest changes", "contest changes", "testament changes"} {
			if search.CheckTextContainsExcludeWords(text, []string{"test"}) {
				t.Fatalf("partial-looking exclusion unexpectedly matched %q", text)
			}
		}
		if !search.CheckTextContainsExcludeWords("unit test passed", []string{"test"}) {
			t.Fatal("whole-word exclusion did not match test")
		}
	})

	t.Run("short terms retain whole-word semantics in multi-term queries", func(t *testing.T) {
		if search.CheckTextContainsAllWordsPartial("go inside document", []string{"go", "in", "doc"}, 50, search.PartialModePrefix) {
			t.Fatal("short term in expanded to inside")
		}
		if !search.CheckTextContainsAllWordsPartial("go in document", []string{"go", "in", "doc"}, 50, search.PartialModePrefix) {
			t.Fatal("whole-word in and partial doc should match")
		}
	})

	t.Run("inline glob does not expand unmarked secondary terms", func(t *testing.T) {
		if !search.CheckTextContainsAllWordsPartial("deployment test", []string{"deploy*", "test"}, 50, search.PartialModeOff) {
			t.Fatal("deploy* and whole-word test should match")
		}
		if search.CheckTextContainsAllWordsPartial("deployment testing", []string{"deploy*", "test"}, 50, search.PartialModeOff) {
			t.Fatal("unmarked test expanded to testing")
		}
	})
}
