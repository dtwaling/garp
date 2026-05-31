package search_test

import (
	"strings"
	"testing"

	"garp/search"
)

func TestExtractorRegistry_RegistersPDFExtractor(t *testing.T) {
	registry := search.NewExtractorRegistry()
	extractor, ok := registry.GetExtractor("pdf")
	if !ok {
		t.Fatal("expected PDF extractor to be registered")
	}

	text, err := extractor.ExtractText([]byte(minimalPDFWithText))
	if err != nil {
		t.Fatalf("unexpected PDF extraction error: %v", err)
	}
	if !strings.Contains(text, "provider access_token") {
		t.Fatalf("expected extracted text to contain query terms, got %q", text)
	}
}

const minimalPDFWithText = `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>
endobj
4 0 obj
<< /Length 56 >>
stream
BT
/F1 24 Tf
72 720 Td
(provider access_token pdf hit) Tj
ET
endstream
endobj
5 0 obj
<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>
endobj
xref
0 6
0000000000 65535 f
0000000009 00000 n
0000000058 00000 n
0000000115 00000 n
0000000241 00000 n
0000000347 00000 n
trailer
<< /Size 6 /Root 1 0 R >>
startxref
417
%%EOF
`
