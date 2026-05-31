package search

import (
	"strings"
	"testing"

	"github.com/ledongthuc/pdf"
)

func TestPDFTextItemsToString_ReconstructsGlyphSpacing(t *testing.T) {
	items := []pdf.Text{
		{X: 0, Y: 100, W: 6, FontSize: 12, S: "H"},
		{X: 6, Y: 100, W: 6, FontSize: 12, S: "a"},
		{X: 12, Y: 100, W: 6, FontSize: 12, S: "m"},
		{X: 18, Y: 100, W: 6, FontSize: 12, S: "i"},
		{X: 24, Y: 100, W: 6, FontSize: 12, S: "l"},
		{X: 30, Y: 100, W: 6, FontSize: 12, S: "t"},
		{X: 36, Y: 100, W: 6, FontSize: 12, S: "o"},
		{X: 42, Y: 100, W: 6, FontSize: 12, S: "n"},
		{X: 48, Y: 100, W: 6, FontSize: 12, S: "i"},
		{X: 54, Y: 100, W: 6, FontSize: 12, S: "a"},
		{X: 60, Y: 100, W: 6, FontSize: 12, S: "n"},
		{X: 76, Y: 100, W: 6, FontSize: 12, S: "c"},
		{X: 82, Y: 100, W: 6, FontSize: 12, S: "y"},
		{X: 88, Y: 100, W: 6, FontSize: 12, S: "c"},
		{X: 94, Y: 100, W: 6, FontSize: 12, S: "l"},
		{X: 100, Y: 100, W: 6, FontSize: 12, S: "e"},
		{X: 106, Y: 100, W: 6, FontSize: 12, S: "s"},
		{X: 0, Y: 84, W: 6, FontSize: 12, S: "i"},
		{X: 6, Y: 84, W: 6, FontSize: 12, S: "m"},
		{X: 12, Y: 84, W: 6, FontSize: 12, S: "p"},
		{X: 18, Y: 84, W: 6, FontSize: 12, S: "o"},
		{X: 24, Y: 84, W: 6, FontSize: 12, S: "s"},
		{X: 30, Y: 84, W: 6, FontSize: 12, S: "s"},
		{X: 36, Y: 84, W: 6, FontSize: 12, S: "i"},
		{X: 42, Y: 84, W: 6, FontSize: 12, S: "b"},
		{X: 48, Y: 84, W: 6, FontSize: 12, S: "l"},
		{X: 54, Y: 84, W: 6, FontSize: 12, S: "e"},
	}

	got := pdfTextItemsToString(items)
	if !strings.Contains(got, "Hamiltonian cycles") {
		t.Fatalf("expected word gap without letter spacing, got %q", got)
	}
	if !strings.Contains(got, "\nimpossible") {
		t.Fatalf("expected line break before impossible, got %q", got)
	}
}
