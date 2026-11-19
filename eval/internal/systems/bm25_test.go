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

func TestCleanTerm(t *testing.T) {
	for _, tc := range []struct{ path, term, want string }{
		{"m.html", "Olk10SideProps", "Olk10 SideProps"},
		{"m.go", "Olk10SideProps", "Olk10SideProps"},
		{"m.html", "cashier*", "cashier*"},
	} {
		if got := CleanTerm(tc.path, tc.term); got != tc.want {
			t.Errorf("CleanTerm(%q, %q) = %q, want %q", tc.path, tc.term, got, tc.want)
		}
	}
}

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
		{[]string{"prune|apple"}, []string{"a.txt", "b.txt"}},
		{[]string{"app*|carrot"}, []string{"b.txt", "a.txt", "c.txt"}}, // equal TF/length in a and c: path tie-break
		{[]string{"absent"}, nil},
		{nil, nil},
	} {
		if got := idx.Search(tc.terms); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v: %v, want %v", tc.terms, got, tc.want)
		}
	}
	for _, tc := range []struct{ terms, want []string }{
		{[]string{"apple|app*", "apple"}, []string{"apple"}},
		{[]string{"apple", "app*"}, []string{"apple", "apple"}},
	} {
		tokens := idx.queryTokens(tc.terms)
		if !reflect.DeepEqual(tokens, tc.want) || idx.score(idx.docs[0], tokens) != idx.score(idx.docs[0], tc.want) {
			t.Fatalf("query tokens %v: %v, want %v", tc.terms, tokens, tc.want)
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
	root := writeCorpus(t, map[string]string{
		"literal.go":  "left <identifier> right",
		"markup.html": "left <identifier> right<script>scriptsecret</script><style>stylesecret</style> &lt;escaped&gt;",
		"markup.xml":  "<root>left <identifier>right</identifier><script>scriptsecret</script><style>stylesecret</style> &lt;escaped&gt;</root>",
		"mail.eml":    "Subject: Reader\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nQWxwaGE=\r\n",
		"bad.docx":    "not a zip",
	})
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
	paths := []string{"reader.docx", "literal.go", "markup.html", "markup.xml", "mail.eml", "bad.docx"}
	idx, err := BuildIndex(root, ".", paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want map[string]bool
	}{
		{"reader.docx", map[string]bool{"reader": true, "alpha": true}},
		{"mail.eml", map[string]bool{"reader": true, "alpha": true}},
		{"literal.go", map[string]bool{"left": true, "identifier": true, "right": true}},
		{"markup.html", map[string]bool{"left": true, "right": true, "escaped": true}},
		{"markup.xml", map[string]bool{"left": true, "right": true, "escaped": true}},
		{"bad.docx", map[string]bool{}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := idx.Tokens(tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tokens %v, want %v", got, tc.want)
			}
		})
	}
	if len(idx.docs) != len(paths) {
		t.Fatal("failed extraction must stay as an empty document")
	}
	if _, err := BuildIndex(root, ".", []string{"missing.txt"}); err == nil {
		t.Fatal("missing file is a setup error")
	}
	for _, path := range []string{"large.txt", "large.html", "large.xml"} {
		t.Run(path, func(t *testing.T) {
			root := writeCorpus(t, map[string]string{path: "inside " + strings.Repeat(" ", textMaxBytes-len("inside ")) + "outside"})
			idx, err := BuildIndex(root, ".", []string{path})
			if err != nil {
				t.Fatal(err)
			}
			if !idx.Tokens(path)["inside"] || idx.Tokens(path)["outside"] {
				t.Fatal("raw text must stop at the engine's byte cap")
			}
		})
	}
}
