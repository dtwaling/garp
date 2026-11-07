package search_test

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestExtractorsRejectCorruptedContainersWithoutRawByteFallback(t *testing.T) {
	const marker = "provider"
	makeZIP := func(name string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(" " + marker + " ")); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	for _, ext := range []string{"pdf", "docx", "odt"} {
		cases := map[string][]byte{"corrupt": []byte("not a container " + marker)}
		if ext == "pdf" {
			cases["truncated"] = []byte("%PDF-1.4\n" + marker)
		} else {
			name := "word/document.xml"
			if ext == "odt" {
				name = "content.xml"
			}
			data := makeZIP(name)
			cases["truncated"] = data[:bytes.Index(data, []byte("PK\x01\x02"))]
			cases["missing XML"] = makeZIP("other.xml")
			corrupt := append([]byte(nil), data...)
			corrupt[bytes.Index(corrupt, []byte(marker))-1] = '\n'
			cases["bad checksum"] = corrupt
		}
		for name, data := range cases {
			t.Run(ext+"/"+name, func(t *testing.T) {
				extractor, _ := search.NewExtractorRegistry().GetExtractor(ext)
				text, err := extractor.ExtractText(data)
				if err == nil || text != "" {
					t.Errorf("extraction = (%q, %v), want empty text and error", text, err)
				}
				path := filepath.Join(t.TempDir(), "broken."+ext)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				engine := search.NewSearchEngine([]string{marker}, nil, nil, false, 1, 1000)
				engine.Silent = true
				matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) != 0 {
					t.Errorf("raw bytes leaked into %d matches", len(matches))
				}
				// Seed a valid Stage 2 match, then corrupt the file to exercise
				// Stage 3 rejection independently of Stage 2 filtering.
				valid := []byte(minimalPDFWithText)
				if ext == "docx" {
					valid = makeZIP("word/document.xml")
				} else if ext == "odt" {
					valid = makeZIP("content.xml")
				}
				if err := os.WriteFile(path, valid, 0o600); err != nil {
					t.Fatal(err)
				}
				matches, err = engine.FilterCandidates([]string{path}, 1, time.Now())
				if err != nil || len(matches) != 1 {
					t.Fatalf("valid seed matches = %d, error = %v", len(matches), err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				results, err := engine.ExtractAndBuildResults(matches)
				if err != nil {
					t.Fatal(err)
				}
				if len(results) != 0 {
					t.Errorf("raw bytes leaked into results: %+v", results)
				}
			})
		}
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
<< /Length 60 >>
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
0000000351 00000 n
trailer
<< /Size 6 /Root 1 0 R >>
startxref
421
%%EOF
`
