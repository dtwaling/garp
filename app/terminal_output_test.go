package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"garp/internal/terminaltext"
	"garp/search"
)

func TestTerminalSanitizeAllControls(t *testing.T) {
	for r := rune(0); r <= 0x9f; r++ {
		if !unicode.IsControl(r) {
			continue
		}
		for _, text := range []string{string(r), string([]byte{byte(r)})} {
			got := terminaltext.Sanitize("before" + text)
			if strings.TrimSpace(got) != "before" {
				t.Errorf("control %U: got %q", r, got)
			}
		}
	}
}

func TestTerminalCLIErrorSanitized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing\x1b]52;c;SECRET\a\b\r.txt")
	out, code := captureIssue4RunStderr(t, []string{"garp", "needle", "--startdir", path})
	if code != 1 || !strings.Contains(out, "Error:") {
		t.Fatalf("code=%d, output=%q", code, out)
	}
	assertSafeTerminalView(t, out)
}

func TestTerminalSearchWarningsQuotePathsAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing\x1b]52;c;SECRET\a\b\r.txt")
	engine := search.NewSearchEngineWithWorkers([]string{"needle"}, nil, []string{".txt"}, false, 1, 1000, 1)
	out := string(captureCLIStdout(t, func() int {
		_, err := engine.FilterCandidates([]string{path}, 1, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return 0
	}))
	if !strings.Contains(out, "Warning: Error checking file") || !strings.Contains(out, `\x1b]52`) {
		t.Fatalf("missing escaped warning: %q", out)
	}
	for _, r := range out {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("warning contains control %U: %q", r, out)
		}
	}
}
