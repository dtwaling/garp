package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"garp/internal/terminaltext"
	"garp/search"
	"github.com/charmbracelet/x/ansi"
)

func TestTerminalSanitize(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"plain Unicode", "café/日本語 🐈.txt", "café/日本語 🐈.txt"},
		{"CSI", "a\x1b[2J\x1b[31mb\x1b[0m", "ab"},
		{"OSC52 BEL", "a\x1b]52;c;SECRET\ab", "ab"},
		{"OSC52 ST", "a\x1b]52;c;SECRET\x1b\\b", "ab"},
		{"OSC8", "a\x1b]8;;https://evil.test\x1b\\link\x1b]8;;\x1b\\b", "alinkb"},
		{"C1 CSI", "a\x9b2Jb", "ab"},
		{"UTF8 C1 CSI", "a\u009b2Jb", "ab"},
		{"C1 OSC", "a\x9d52;c;SECRET\x9cb", "ab"},
		{"UTF8 C1 OSC", "a\u009d52;c;SECRET\u009cb", "ab"},
		{"controls", "a\x00\a\b\r\n\t\x7f\u0085b", "a   b"},
		{"DCS", "a\x1bPSECRET\x1b\\b", "ab"},
		{"unterminated OSC", "a\x1b]52;c;SECRET", "a"},
		{"unterminated CSI", "a\x1b[31", "a"},
		{"escape", "a\x1b", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := terminaltext.Sanitize(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

const terminalAttack = "\x1b[2J\x1b]52;c;SECRET\a\x1b]8;;https://evil.test\x1b\\\x1b]8;;\x1b\\\b\x7f\u009b2J"

func assertSafeTerminalView(t *testing.T, view string) {
	t.Helper()
	if strings.Contains(view, "SECRET") || strings.Contains(view, "evil.test") || strings.Contains(view, "\x1b[2J") {
		t.Fatalf("injected sequence in %q", view)
	}
	for _, r := range ansi.Strip(view) {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("control %U in %q", r, view)
		}
	}
}

func TestTUIProgressSanitizes(t *testing.T) {
	progressMu.Lock()
	old, have := latestProgress, haveLatestProgress
	progressMu.Unlock()
	t.Cleanup(func() { progressMu.Lock(); latestProgress, haveLatestProgress = old, have; progressMu.Unlock() })
	for _, stage := range []string{"discovery", "processing"} {
		for _, total := range []int{0, 10} {
			for _, tick := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/tick=%t", stage, total, tick), func(t *testing.T) {
					msg := progressMsg{Stage: stage, Count: 1, Total: total, Path: "safe" + terminalAttack + ".txt"}
					m := model{loading: true, width: 200, height: 60}
					var updated model
					if tick {
						progressMu.Lock()
						latestProgress, haveLatestProgress = msg, true
						progressMu.Unlock()
						got, _ := m.Update(progressTick{})
						updated = got.(model)
					} else {
						got, _ := m.Update(msg)
						updated = got.(model)
					}
					want := fmt.Sprintf("%s [1]: safe.txt", strings.Title(stage))
					if total > 0 {
						want = fmt.Sprintf("%s [1/10]: safe.txt", strings.Title(stage))
					}
					if updated.progressText != want {
						t.Fatalf("got %q, want %q", updated.progressText, want)
					}
					assertSafeTerminalView(t, updated.View())
				})
			}
		}
	}
}

func TestTUIViewSanitizesAndRetainsHighlighting(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw=%t", raw), func(t *testing.T) {
			lastContentHeight, lastExcerptInnerWidth = 0, 0
			result := search.SearchResult{FilePath: "safe" + terminalAttack + ".txt", EmailSubject: "subject" + terminalAttack, EmailDate: "date" + terminalAttack, Excerpts: []string{"needle " + terminalAttack + "visible"}, CleanContent: "extra " + terminalAttack + "context", MatchedTerms: []string{"needle", "extra"}}
			if raw {
				result.RawExcerpts = []string{"needle " + terminalAttack + "visible"}
			}
			m := model{results: []search.SearchResult{result}, searchWords: []string{"needle", "extra", terminalAttack}, excludeWords: []string{terminalAttack, ".txt" + terminalAttack}, width: 200, height: 80}
			view := m.View()
			assertSafeTerminalView(t, view)
			for _, want := range []string{"File: safe.txt", "Subject: subject", "Date: date", "visible", "context", "Excerpt 1:", "\x1b[1;31mneedle\x1b[0m"} {
				if !strings.Contains(view, want) {
					t.Fatalf("missing %q in %q", want, view)
				}
			}
			if m.results[0].FilePath != result.FilePath {
				t.Fatal("render mutated stored path")
			}
		})
	}
}

func TestSanitizeWithOffsetsTranslatesSpans(t *testing.T) {
	raw := "prefix " + terminalAttack + "ms-" + "\x1b[31m" + "aaaa" + "\u009b2J" + " omega"
	prefixEnd := len("prefix " + terminalAttack)
	wildcardEnd := prefixEnd + len("ms-"+"\x1b[31m"+"aaaa"+"\u009b2J")
	omegaStart := wildcardEnd + 1
	omegaEnd := len(raw)

	sanitized, offsets := terminaltext.SanitizeWithOffsets(raw)
	if sanitized != "prefix ms-aaaa omega" {
		t.Fatalf("sanitized = %q, want %q", sanitized, "prefix ms-aaaa omega")
	}
	if len(offsets) != len(raw)+1 {
		t.Fatalf("len(offsets) = %d, want %d", len(offsets), len(raw)+1)
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] < offsets[i-1] {
			t.Fatalf("offsets not monotonic at %d: %d < %d", i, offsets[i], offsets[i-1])
		}
	}
	if got := sanitized[offsets[prefixEnd]:offsets[wildcardEnd]]; got != "ms-aaaa" {
		t.Fatalf("translated wildcard span = %q, want %q", got, "ms-aaaa")
	}
	if got := sanitized[offsets[omegaStart]:offsets[omegaEnd]]; got != "omega" {
		t.Fatalf("translated omega span = %q, want %q", got, "omega")
	}
}

func TestTUIViewPreservesValidatedMatchRangesAcrossSanitization(t *testing.T) {
	t.Run("slice boundary suffix is not falsely highlighted", func(t *testing.T) {
		lastContentHeight, lastExcerptInnerWidth = 0, 0
		rawExcerpt := "api " + terminalAttack + "then api"
		trueStart := len("api " + terminalAttack + "then ")
		trueEnd := len(rawExcerpt)
		result := search.SearchResult{
			FilePath:       "boundary.go",
			FileSize:       64,
			Score:          3,
			TermCount:      1,
			MatchedTerms:   []string{"api"},
			RawExcerpts:    []string{rawExcerpt},
			ExcerptMatches: [][]search.MatchRange{{{Start: trueStart, End: trueEnd}}},
		}
		m := model{
			results:     []search.SearchResult{result},
			searchWords: []string{"api"},
			width:       200,
			height:      80,
		}
		view := m.View()
		assertSafeTerminalView(t, view)
		want := "api then \x1b[1;31mapi\x1b[0m"
		if !strings.Contains(view, want) {
			t.Fatalf("expected only source-validated match highlighted (%q), got view:\n%s", want, view)
		}
		if strings.Contains(view, "\x1b[1;31mapi\x1b[0m then") {
			t.Fatalf("cropped suffix at slice start was falsely highlighted as whole word:\n%s", view)
		}
	})

	t.Run("clipped wildcard match retains highlight after sanitization", func(t *testing.T) {
		lastContentHeight, lastExcerptInnerWidth = 0, 0
		rawExcerpt := terminalAttack + "ms-" + strings.Repeat("a", 40) + " ... omega"
		wildcardStart := len(terminalAttack)
		wildcardEnd := wildcardStart + len("ms-") + 40
		omegaStart := strings.LastIndex(rawExcerpt, "omega")
		omegaEnd := omegaStart + len("omega")

		result := search.SearchResult{
			FilePath:        "wildcard.go",
			FileSize:        256,
			Score:           12,
			TermCount:       2,
			MatchedTerms:    []string{"ms-*-api", "omega"},
			RawExcerpts:     []string{rawExcerpt},
			ChunkTermCounts: []int{2},
			ExcerptMatches: [][]search.MatchRange{{
				{Start: wildcardStart, End: wildcardEnd},
				{Start: omegaStart, End: omegaEnd},
			}},
		}
		m := model{
			results:     []search.SearchResult{result},
			searchWords: []string{"ms-*-api", "omega"},
			width:       200,
			height:      80,
		}
		view := m.View()
		assertSafeTerminalView(t, view)
		wantWildcard := "\x1b[1;31mms-" + strings.Repeat("a", 40) + "\x1b[0m"
		wantOmega := "\x1b[1;31momega\x1b[0m"
		if !strings.Contains(view, wantWildcard) {
			t.Fatalf("clipped wildcard highlight %q missing from view:\n%s", wantWildcard, view)
		}
		if !strings.Contains(view, wantOmega) {
			t.Fatalf("omega highlight %q missing from view:\n%s", wantOmega, view)
		}
	})

	t.Run("end-to-end SearchEngine clipped wildcard excerpt highlights in TUI View", func(t *testing.T) {
		lastContentHeight, lastExcerptInnerWidth = 0, 0
		priorBudget := search.ExcerptCharBudget
		search.ExcerptCharBudget = func() int { return 200 }
		t.Cleanup(func() { search.ExcerptCharBudget = priorBudget })

		dir := t.TempDir()
		token := "ms-" + strings.Repeat("a", 120) + "-api"
		content := token + strings.Repeat(" z", 120) + " omega"
		file := filepath.Join(dir, "sample.go")
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		se := search.NewSearchEngine([]string{"ms-*-api", "omega"}, nil, []string{".go"}, false, 1, 1000)
		se.StartDir = dir
		se.IncludeCode = true
		se.Silent = true
		results, err := se.Execute()
		if err != nil || len(results) != 1 {
			t.Fatalf("Execute() = %v, len=%d", err, len(results))
		}
		m := model{
			results:     results,
			searchWords: []string{"ms-*-api", "omega"},
			width:       200,
			height:      80,
		}
		view := m.View()
		assertSafeTerminalView(t, view)
		if !strings.Contains(view, "\x1b[1;31mms-aaaa") {
			t.Fatalf("expected clipped wildcard prefix highlighted in TUI View:\n%s", view)
		}
		if !strings.Contains(view, "\x1b[1;31momega\x1b[0m") {
			t.Fatalf("expected omega highlighted in TUI View:\n%s", view)
		}
	})
}
