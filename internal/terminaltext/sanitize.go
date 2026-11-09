// Package terminaltext neutralizes untrusted text before terminal styling.
package terminaltext

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi/parser"
)

// Sanitize removes terminal sequences and controls. Whitespace controls become
// spaces so untrusted text cannot create new UI lines. Call before highlighting
// or styling, never on a finished UI frame. It does not change stored paths.
func Sanitize(text string) string {
	sanitized, _ := SanitizeWithOffsets(text)
	return sanitized
}

// SanitizeWithOffsets removes terminal sequences and controls identically to
// Sanitize and returns an offset map of length len(text)+1. For any byte
// position pos in text (0 <= pos <= len(text)), offsets[pos] is the translated
// byte position in the returned sanitized string.
func SanitizeWithOffsets(text string) (string, []int) {
	offsets := make([]int, len(text)+1)
	if len(text) == 0 {
		return "", offsets
	}

	// Stage 1: The ANSI parser recognizes byte-form C1 controls. Normalize UTF-8
	// C1 runes too, without treating continuation bytes in other Unicode as
	// controls, while recording each normalized byte's original [start, end)
	// span in text.
	norm := make([]byte, 0, len(text))
	normOrigStart := make([]int, 0, len(text))
	normOrigEnd := make([]int, 0, len(text))
	for origIdx := 0; origIdx < len(text); {
		r, size := utf8.DecodeRuneInString(text[origIdx:])
		if r >= 0x80 && r <= 0x9f {
			norm = append(norm, byte(r))
			normOrigStart = append(normOrigStart, origIdx)
			normOrigEnd = append(normOrigEnd, origIdx+size)
		} else {
			for k := 0; k < size; k++ {
				norm = append(norm, text[origIdx+k])
				normOrigStart = append(normOrigStart, origIdx+k)
				normOrigEnd = append(normOrigEnd, origIdx+k+1)
			}
		}
		origIdx += size
	}

	// Stage 2: Strip ANSI sequences using the DEC ANSI parser state machine
	// (matching ansi.Strip) while preserving original byte span provenance.
	stripped := make([]byte, 0, len(norm))
	strippedOrigStart := make([]int, 0, len(norm))
	strippedOrigEnd := make([]int, 0, len(norm))
	var (
		ri     int
		rw     int
		pstate = parser.GroundState
	)
	for i, b := range norm {
		if pstate == parser.Utf8State {
			stripped = append(stripped, b)
			strippedOrigStart = append(strippedOrigStart, normOrigStart[i])
			strippedOrigEnd = append(strippedOrigEnd, normOrigEnd[i])
			ri++
			if ri < rw {
				continue
			}
			pstate = parser.GroundState
			ri = 0
			rw = 0
			continue
		}

		state, action := parser.Table.Transition(pstate, b)
		switch action {
		case parser.CollectAction:
			if state == parser.Utf8State {
				rw = utf8ByteLen(b)
				stripped = append(stripped, b)
				strippedOrigStart = append(strippedOrigStart, normOrigStart[i])
				strippedOrigEnd = append(strippedOrigEnd, normOrigEnd[i])
				ri++
			}
		case parser.PrintAction, parser.ExecuteAction:
			stripped = append(stripped, b)
			strippedOrigStart = append(strippedOrigStart, normOrigStart[i])
			strippedOrigEnd = append(strippedOrigEnd, normOrigEnd[i])
		}

		if pstate != parser.Utf8State {
			pstate = state
		}
	}

	// Stage 3: Drop invalid UTF-8 and non-whitespace controls; map \n, \r, \t to
	// single spaces while populating the byte offset table.
	var out strings.Builder
	out.Grow(len(stripped))
	origCursor := 0
	for idx := 0; idx < len(stripped); {
		r, size := utf8.DecodeRune(stripped[idx:])
		origStart := strippedOrigStart[idx]
		origEnd := strippedOrigEnd[idx+size-1]
		for p := origCursor; p <= origStart; p++ {
			offsets[p] = out.Len()
		}
		if r == utf8.RuneError && size == 1 {
			for p := origStart; p <= origEnd; p++ {
				offsets[p] = out.Len()
			}
			origCursor = origEnd
			idx += size
			continue
		}
		switch r {
		case '\n', '\r', '\t':
			out.WriteByte(' ')
			for p := origStart + 1; p <= origEnd; p++ {
				offsets[p] = out.Len()
			}
			origCursor = origEnd
		default:
			if unicode.IsControl(r) {
				for p := origStart; p <= origEnd; p++ {
					offsets[p] = out.Len()
				}
				origCursor = origEnd
			} else {
				for k := 0; k < size; k++ {
					bStart := strippedOrigStart[idx+k]
					bEnd := strippedOrigEnd[idx+k]
					for p := origCursor; p <= bStart; p++ {
						offsets[p] = out.Len()
					}
					out.WriteByte(stripped[idx+k])
					for p := bStart + 1; p <= bEnd; p++ {
						offsets[p] = out.Len()
					}
					origCursor = bEnd
				}
			}
		}
		idx += size
	}
	for p := origCursor; p <= len(text); p++ {
		offsets[p] = out.Len()
	}
	return out.String(), offsets
}

func utf8ByteLen(b byte) int {
	if b <= 0b0111_1111 {
		return 1
	} else if b >= 0b1100_0000 && b <= 0b1101_1111 {
		return 2
	} else if b >= 0b1110_0000 && b <= 0b1110_1111 {
		return 3
	} else if b >= 0b1111_0000 && b <= 0b1111_0111 {
		return 4
	}
	return -1
}
