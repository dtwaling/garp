package search

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalizeMappedWhitespacePreservesOffsets(t *testing.T) {
	// Cover every byte (including invalid UTF-8), all C1 control runes, and
	// multibyte text/whitespace. Compare against the former two-pass mapping.
	var all strings.Builder
	for i := 0; i < 256; i++ {
		all.WriteByte(byte(i))
		all.WriteString("x \t")
	}
	for r := rune(0x7f); r <= 0x9f; r++ {
		all.WriteRune(r)
		all.WriteString("\n \x00")
	}
	for _, raw := range []string{"", "plain", " \x00\t\r\n\u0085 ", "\u00a0é\u2003\t\u009f界\xff\x80", all.String()} {
		m := mappedContent{text: raw, offsets: make([]int, len(raw))}
		for i := range m.offsets {
			m.offsets[i] = 7 + 3*i
		}
		want := m.replace(controlCharRegex, " ").replace(whitespaceRegex, " ")
		got := normalizeMappedWhitespace(m)
		if got.text != want.text || !slices.Equal(got.offsets, want.offsets) {
			t.Fatalf("raw=%q: got=%+v want=%+v", raw, got, want)
		}
		textOnly := normalizeMappedWhitespace(mappedContent{text: raw})
		if textOnly.text != want.text || textOnly.offsets != nil {
			t.Fatalf("text-only normalization differs or allocated offsets: %+v", textOnly)
		}
	}
}
