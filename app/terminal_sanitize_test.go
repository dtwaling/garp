package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

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

	t.Run("fragment slicing aligns UTF-8 rune boundaries without exposing C1 continuation bytes", func(t *testing.T) {
		lastContentHeight, lastExcerptInnerWidth = 0, 0
		// 'ě' is 0xc4 0x9b; placing "needle" at byte 121 makes loc[0]-120 == 1
		// (the 0x9b C1 CSI continuation byte), and placing 'ě' at loc[1]+119
		// makes loc[1]+120 land mid-rune at the end boundary as well.
		clean := "ě2J" + strings.Repeat("x", 116) + " needle " + strings.Repeat("y", 118) + "ě99"
		result := search.SearchResult{
			FilePath:       "utf8slice.txt",
			FileSize:       int64(len(clean)),
			Score:          11,
			TermCount:      2,
			MatchedTerms:   []string{"alpha", "needle"},
			RawExcerpts:    []string{"alpha"},
			ExcerptMatches: [][]search.MatchRange{{{Start: 0, End: len("alpha"), Term: "alpha"}}},
			CleanContent:   clean,
		}
		m := model{
			results:     []search.SearchResult{result},
			searchWords: []string{"alpha", "needle"},
			width:       200,
			height:      80,
		}
		view := m.View()
		if !utf8.ValidString(view) {
			t.Fatalf("TUI view is not valid UTF-8: %q", view)
		}
		if strings.Contains(view, "\x9b2J") {
			t.Fatalf("fragment slice exposed raw C1 CSI sequence \\x9b2J in view: %q", view)
		}
		assertSafeTerminalView(t, view)
		if !strings.Contains(view, "\x1b[1;31mneedle\x1b[0m") {
			t.Fatalf("expected augmented needle highlight in view:\n%s", view)
		}
	})

	t.Run("term completely removed by excerpt truncation is augmented from CleanContent", func(t *testing.T) {
		lastContentHeight, lastExcerptInnerWidth = 0, 0
		// ChunkTermCounts[0] is 2 (from the pre-truncation source window), but
		// truncation removed "omega" from RawExcerpts[0] and ExcerptMatches[0],
		// while the surviving clipped wildcard match retains Term: "ms-*-api".
		rawExcerpt := "ms-" + strings.Repeat("a", 40)
		clean := "ms-" + strings.Repeat("a", 40) + "-api " + strings.Repeat("z ", 80) + "omega tail context"
		result := search.SearchResult{
			FilePath:        "truncated_term.go",
			FileSize:        int64(len(clean)),
			Score:           12,
			TermCount:       2,
			MatchedTerms:    []string{"ms-*-api", "omega"},
			RawExcerpts:     []string{rawExcerpt},
			ChunkTermCounts: []int{2},
			ExcerptMatches: [][]search.MatchRange{{
				{Start: 0, End: len(rawExcerpt), Term: "ms-*-api"},
			}},
			CleanContent: clean,
		}
		m := model{
			results:     []search.SearchResult{result},
			searchWords: []string{"ms-*-api", "omega"},
			width:       200,
			height:      80,
		}
		view := m.View()
		assertSafeTerminalView(t, view)
		if !strings.Contains(view, "\x1b[1;31mms-"+strings.Repeat("a", 40)+"\x1b[0m") {
			t.Fatalf("missing clipped wildcard highlight in view:\n%s", view)
		}
		if !strings.Contains(view, "\x1b[1;31momega\x1b[0m tail context") {
			t.Fatalf("expected truncated term 'omega' to be augmented from CleanContent, got view:\n%s", view)
		}
		if strings.Count(view, "ms-"+strings.Repeat("a", 40)) != 1 {
			t.Fatalf("clipped wildcard term already present in Excerpt 1 should not be re-augmented:\n%s", view)
		}

		// End-to-end SearchEngine verification: 5-term multipart excerpt where
		// perTerm=60 produces >240 bytes and truncateExcerpt drops the 5th term
		// ("omega") while ChunkTermCounts[0] remains 5.
		priorBudget := search.ExcerptCharBudget
		search.ExcerptCharBudget = func() int { return 240 }
		t.Cleanup(func() { search.ExcerptCharBudget = priorBudget })

		dir := t.TempDir()
		pad := strings.Repeat("x", 30)
		sep := strings.Repeat(" z", 90) + " "
		fileContent := fmt.Sprintf("%s alpha %s%s%s bravo %s%s%s charlie %s%s%s delta %s%s%s omega %s",
			pad, pad, sep, pad, pad, sep, pad, pad, sep, pad, pad, sep, pad, pad)
		file := filepath.Join(dir, "five_terms.go")
		if err := os.WriteFile(file, []byte(fileContent), 0o600); err != nil {
			t.Fatal(err)
		}
		terms := []string{"alpha", "bravo", "charlie", "delta", "omega"}
		se := search.NewSearchEngine(terms, nil, []string{".go"}, false, 1, 2000)
		se.StartDir = dir
		se.IncludeCode = true
		se.Silent = true
		results, err := se.Execute()
		if err != nil || len(results) != 1 {
			t.Fatalf("Execute() = %v, len=%d", err, len(results))
		}
		if results[0].ChunkTermCounts[0] != 5 {
			t.Fatalf("ChunkTermCounts[0] = %d, want 5", results[0].ChunkTermCounts[0])
		}
		if strings.Contains(results[0].RawExcerpts[0], "omega") {
			t.Fatalf("expected RawExcerpts[0] to have 'omega' truncated away: %q", results[0].RawExcerpts[0])
		}
		lastContentHeight, lastExcerptInnerWidth = 0, 0
		mE2E := model{
			results:     results,
			searchWords: terms,
			width:       200,
			height:      80,
		}
		e2eView := mE2E.View()
		assertSafeTerminalView(t, e2eView)
		if !strings.Contains(e2eView, "\x1b[1;31momega\x1b[0m") {
			t.Fatalf("expected truncated 5th term 'omega' to be augmented and highlighted in TUI View:\n%s", e2eView)
		}
	})

	t.Run("sanitization does not manufacture CleanContent matches from stripped invalid bytes", func(t *testing.T) {
		lastContentHeight, lastExcerptInnerWidth = 0, 0
		// "ome\x80ga" becomes "omega" after sanitization, but was not a match in
		// the source CleanContent. Augmentation must skip the fabricated match
		// and select the window around the actual "omega" match later in CleanContent.
		clean := "ome\x80ga bogus prefix " + strings.Repeat("x ", 120) + "actual omega target suffix"
		result := search.SearchResult{
			FilePath:       "invalid_utf8_clean.txt",
			FileSize:       int64(len(clean)),
			Score:          10,
			TermCount:      2,
			MatchedTerms:   []string{"alpha", "omega"},
			RawExcerpts:    []string{"alpha"},
			ExcerptMatches: [][]search.MatchRange{{{Start: 0, End: len("alpha"), Term: "alpha"}}},
			CleanContent:   clean,
		}
		m := model{
			results:     []search.SearchResult{result},
			searchWords: []string{"alpha", "omega"},
			width:       200,
			height:      80,
		}
		view := m.View()
		assertSafeTerminalView(t, view)
		if !strings.Contains(view, "actual \x1b[1;31momega\x1b[0m target suffix") {
			t.Fatalf("expected augmentation window around actual 'omega' match, got view:\n%s", view)
		}
		if strings.Contains(view, "bogus prefix") {
			t.Fatalf("augmentation falsely selected fabricated 'ome\\x80ga' occurrence:\n%s", view)
		}
	})
}
