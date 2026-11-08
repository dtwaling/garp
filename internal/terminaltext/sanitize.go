// Package terminaltext neutralizes untrusted text before terminal styling.
package terminaltext

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Sanitize removes terminal sequences and controls. Whitespace controls become
// spaces so untrusted text cannot create new UI lines. Call before highlighting
// or styling, never on a finished UI frame. It does not change stored paths.
func Sanitize(text string) string {
	// The ANSI parser recognizes byte-form C1 controls. Normalize UTF-8 C1
	// runes too, without treating continuation bytes in other Unicode as controls.
	var normalized strings.Builder
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		if r >= 0x80 && r <= 0x9f {
			normalized.WriteByte(byte(r))
		} else {
			normalized.WriteString(text[:size])
		}
		text = text[size:]
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(ansi.Strip(normalized.String()), ""))
}
