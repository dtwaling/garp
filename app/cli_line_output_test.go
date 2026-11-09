package app

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode"

	"garp/search"
)

func TestEscapeOutputLine(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"ordinary café.txt", "ordinary café.txt"},
		{"\r\n\t\x00\x1b\x7f\u0085\u2028\u2029", `\r\n\t\x00\x1b\x7f\u0085\u2028\u2029`},
		{`literal\n "quoted"`, `literal\\n \"quoted\"`},
		{string([]byte{0xff, 0x9b}), `\xff\x9b`},
	} {
		if got := escapeOutputLine(tt.input); got != tt.want {
			t.Errorf("escapeOutputLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
	for r := rune(0); r <= 0x9f; r++ {
		if unicode.IsControl(r) && strings.ContainsRune(escapeOutputLine(string(r)), r) {
			t.Errorf("control U+%04X survived escaping", r)
		}
	}
}

func TestPlainOutputCannotForgeRecords(t *testing.T) {
	const payload = "evil\r\nMATCH 9\nFILE: forged\rSIZE: 0\nEXCERPT 9: forged\n---\x1b[2J\x00\u0085\u2028\u2029"
	const escaped = `evil\r\nMATCH 9\nFILE: forged\rSIZE: 0\nEXCERPT 9: forged\n---\x1b[2J\x00\u0085\u2028\u2029`
	args := parseArguments([]string{"alpha"})
	outcome := search.ExecutionResult{
		Returned: 1,
		Results: []search.SearchResult{{
			FilePath: payload, FileSize: 42,
			RawExcerpts:     []string{payload, payload, payload, payload},
			StartLines:      []int{2, 0, 3, 0},
			ChunkScores:     []int{1, 1, 0, 0},
			ChunkTermCounts: []int{1, 1, 0, 0},
		}},
	}
	var out bytes.Buffer
	writePlainOutput(&out, args, outcome)
	want := "MATCH 1/1 [Score: 0 (0/1 terms)]\nFILE: " + escaped + "\nSIZE: 42\n" +
		"EXCERPT 1 [L2, Score: 1 (1 terms)]: " + escaped + "\n" +
		"EXCERPT 2 [Score: 1 (1 terms)]: " + escaped + "\n" +
		"EXCERPT 3 [L3]: " + escaped + "\n" +
		"EXCERPT 4: " + escaped + "\n---\n"
	if out.String() != want {
		t.Fatalf("plain output = %q, want %q", out.String(), want)
	}
	if outcome.Results[0].FilePath != payload || outcome.Results[0].RawExcerpts[0] != payload {
		t.Fatal("output escaping mutated search results")
	}
}

func TestPathOnlyCannotForgeLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support control characters in filenames")
	}
	root := t.TempDir()
	name := "evil\r\nMATCH 9\nFILE: forged\nSIZE: 0\nEXCERPT 9\n---\t\x1b\x7f\u0085\u2028.txt"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := parseArguments([]string{"alpha", "--startdir", root, "--only", "txt", "--pathonly"})
	out := string(captureCLIStdout(t, func() int { return runPathOnly(args) }))
	want := root + `/evil\r\nMATCH 9\nFILE: forged\nSIZE: 0\nEXCERPT 9\n---\t\x1b\x7f\u0085\u2028.txt` + "\n"
	if out != want {
		t.Fatalf("pathonly output = %q, want %q", out, want)
	}
}

func TestPlainOutputBackslashPathsRoundTripInParityHelpers(t *testing.T) {
	const winPath = `C:\Users\runner\project\neutral\zz-top.txt`
	args := parseArguments([]string{"alpha"})
	outcome := search.ExecutionResult{
		Returned: 1,
		Results: []search.SearchResult{{
			FilePath:        winPath,
			FileSize:        42,
			RawExcerpts:     []string{"alpha beta", "alpha gamma"},
			StartLines:      []int{1, 10},
			ChunkScores:     []int{10, 8},
			ChunkTermCounts: []int{2, 2},
		}},
	}
	var out bytes.Buffer
	writePlainOutput(&out, args, outcome)

	gotPaths := outputPathsFromPlainOutput(t, out.String())
	if !slicesEqual(gotPaths, []string{winPath}) {
		t.Fatalf("outputPathsFromPlainOutput = %q, want [%q]", gotPaths, winPath)
	}
	if got := excerptCountForPlainFile(out.String(), winPath); got != 2 {
		t.Fatalf("excerptCountForPlainFile(%q) = %d, want 2", winPath, got)
	}
}
