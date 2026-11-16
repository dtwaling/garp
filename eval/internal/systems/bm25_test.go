package systems

import (
	"archive/zip"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"garp/search"
)

func TestBM25(t *testing.T) {
	root := writeCorpus(t, map[string]string{"sub/a.txt": "apple apple banana", "sub/b.txt": "apple carrot", "sub/c.txt": "banana carrot carrot", "other/x.txt": "apple"})
	idx, err := BuildIndex(root, "sub", []string{"other/x.txt", "sub/c.txt", "sub/b.txt", "sub/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// N=3, df(apple)=2, avgdl=8/3. In a: tf=2, dl=3;
	// score=ln(1.6)*4.4/3.3125. In b: tf=1, dl=2.
	wantScore := math.Log(1.6) * 4.4 / 3.3125
	if got := idx.score(idx.docs[0], []string{"apple"}); math.Abs(got-wantScore) > 1e-12 {
		t.Fatalf("score %.15g, want %.15g", got, wantScore)
	}
	for _, tc := range []struct{ terms, want []string }{
		{[]string{"APPLE"}, []string{"a.txt", "b.txt"}},
		{[]string{"app*"}, []string{"a.txt", "b.txt"}},
		{[]string{"absent"}, nil},
		{nil, nil},
	} {
		if got := idx.Search(tc.terms); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v: %v, want %v", tc.terms, got, tc.want)
		}
	}
	if got := tokenize("Foo_bar-Baz.qux CAFÉ 42"); !reflect.DeepEqual(got, []string{"foo", "bar", "baz", "qux", "café", "42"}) {
		t.Fatalf("tokens %v", got)
	}
	if got := idx.Tokens("a.txt"); !reflect.DeepEqual(got, map[string]bool{"apple": true, "banana": true}) {
		t.Fatalf("document tokens: %v", got)
	}
	if len(idx.Tokens("missing")) != 0 {
		t.Fatal("missing file has tokens")
	}
	idx, err = BuildIndex(writeCorpus(t, map[string]string{"z.txt": "alpha alphabet", "a.txt": "alpha alphabet"}), ".", []string{"z.txt", "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Search([]string{"al*"}); !reflect.DeepEqual(got, []string{"a.txt", "z.txt"}) {
		t.Fatalf("ties: %v", got)
	}
	if got := idx.queryTokens([]string{"al*"}); !reflect.DeepEqual(got, []string{"alpha", "alphabet"}) {
		t.Fatalf("expansion: %v", got)
	}
	files := make(map[string]string)
	var paths []string
	for i := 0; i < 51; i++ {
		p := fmt.Sprintf("%02d.txt", i)
		files[p] = "alpha"
		paths = append(paths, p)
	}
	idx, err = BuildIndex(writeCorpus(t, files), ".", paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.Search([]string{"alpha"}); !reflect.DeepEqual(got, paths[:50]) {
		t.Fatalf("top 50: %v", got)
	}
}

func TestBuildIndexExtraction(t *testing.T) {
	root := writeCorpus(t, map[string]string{"literal.go": "left <identifier> right", "markup.html": "left <identifier> right", "bad.docx": "not a zip"})
	file := filepath.Join(root, "reader.docx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Reader Alpha</w:t></w:r></w:p></w:body></w:document>`)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	extractor, _ := search.NewExtractorRegistry().GetExtractor("docx")
	if _, ok := extractor.(search.ReaderAtExtractor); !ok {
		t.Fatal("fixture must exercise ReaderAt dispatch")
	}
	idx, err := BuildIndex(root, ".", []string{"reader.docx", "literal.go", "markup.html", "bad.docx"})
	if err != nil {
		t.Fatal(err)
	}
	if !idx.Tokens("reader.docx")["reader"] || !idx.Tokens("reader.docx")["alpha"] {
		t.Fatalf("DOCX tokens: %v", idx.Tokens("reader.docx"))
	}
	if !idx.Tokens("literal.go")["identifier"] || idx.Tokens("markup.html")["identifier"] {
		t.Fatal("literal and markup cleaners differ")
	}
	if len(idx.Tokens("bad.docx")) != 0 || len(idx.docs) != 4 {
		t.Fatal("failed extraction must stay as an empty document")
	}
	if _, err := BuildIndex(root, ".", []string{"missing.txt"}); err == nil {
		t.Fatal("missing file is a setup error")
	}
	root = writeCorpus(t, map[string]string{"large.txt": "inside " + strings.Repeat(" ", textMaxBytes-len("inside ")) + "outside"})
	idx, err = BuildIndex(root, ".", []string{"large.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !idx.Tokens("large.txt")["inside"] || idx.Tokens("large.txt")["outside"] {
		t.Fatal("plain text must stop at the engine's byte cap")
	}
}
