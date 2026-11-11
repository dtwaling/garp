package search

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/ledongthuc/pdf"
)

func TestMBOXExtractorRequiresValidMessages(t *testing.T) {
	const envelope = "From sender@example.com Mon Jan 1 00:00:00 2024\n"
	for _, tc := range []struct{ name, data, want string }{
		{"empty", "", ""},
		{"non-mbox", "raw unparsed bytes", ""},
		{"invalid MIME", envelope + "invalid header\n\nraw body", ""},
		{"valid", envelope + "Subject: Example\n\nmessage body\n", "Example message body\n---\n"},
		{"multiple", envelope + "Subject: First\n\none\n" + envelope + "Subject: Second\n\ntwo\n", "First one\n---\nSecond two\n---\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&MBOXExtractor{}).ExtractText([]byte(tc.data))
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("extraction = (%q, %v), want %q with error=%t", got, err, tc.want, tc.want == "")
			}
		})
	}
}

func TestEMLExtractorPrependsDecodedSubject(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"plain", "Subject: Plain subject\r\n\r\nbody text", "Plain subject body text"},
		{"rfc2047", "Subject: =?UTF-8?B?Y2Fmw6kgc3ViamVjdA==?=\r\n\r\nbody", "café subject body"},
		{"folded", "Subject: folded\r\n subject\r\n\r\nbody", "folded subject body"},
		{"missing", "Content-Type: text/plain\r\n\r\nbody", "body"},
		{"subject only", "Subject: only\r\n\r\n", "only"},
		{"html only", "Subject: html\r\nContent-Type: text/html\r\n\r\n<p>body</p>", "html body"},
		{"brackets", "Subject: > Re: <tag> 1 < 2\r\n\r\nbody > end", "Re: tag 1 2 body > end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&EMLExtractor{}).ExtractText([]byte(tc.data))
			if err != nil || got != tc.want {
				t.Fatalf("extraction = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

func TestExtractorRegistry_RegistersPDFExtractor(t *testing.T) {
	registry := NewExtractorRegistry()
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
				extractor, _ := NewExtractorRegistry().GetExtractor(ext)
				text, err := extractor.ExtractText(data)
				if err == nil || text != "" {
					t.Errorf("extraction = (%q, %v), want empty text and error", text, err)
				}
				path := filepath.Join(t.TempDir(), "broken."+ext)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{marker}, nil, nil, false, 1, 1000)
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

// guardedReaderAt fails the test even if an extractor swallows the read error.
type guardedReaderAt struct {
	t      *testing.T
	r      io.ReaderAt
	base   int64
	end    int64
	maxEnd int64
}

func (r *guardedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	end := off + int64(len(p))
	if off < r.base || end > r.end {
		r.t.Errorf("unexpected read [%d, %d), allowed [%d, %d)", off, end, r.base, r.end)
		return 0, fmt.Errorf("read outside allowed region")
	}
	r.maxEnd = max(r.maxEnd, end)
	return r.r.ReadAt(p, off-r.base)
}

func readerAtZIP(t *testing.T, name, text string, offset int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.SetOffset(offset)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, text); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReaderAtExtractorLargeVirtualDOCXAndODT(t *testing.T) {
	const offset int64 = 120 * 1024 * 1024
	for ext, name := range map[string]string{"docx": "word/document.xml", "odt": "content.xml"} {
		t.Run(ext, func(t *testing.T) {
			data := readerAtZIP(t, name, "<text>virtual container hit</text>", offset)
			// Keep ZIP's EOF probe wholly in the tail while leaving the middle inaccessible.
			tail := append(make([]byte, 64*1024), data...)
			size := offset + int64(len(data))
			r := &guardedReaderAt{t: t, r: bytes.NewReader(tail), base: offset - 64*1024, end: size}
			e, _ := NewExtractorRegistry().GetExtractor(ext)
			text, err := e.(ReaderAtExtractor).ExtractReaderAt(r, size, 4096)
			if err != nil || text != "virtual container hit" {
				t.Fatalf("extraction = (%q, %v)", text, err)
			}
		})
	}
}

func TestReaderAtExtractorMaxInt64Budget(t *testing.T) {
	for _, ext := range []string{"docx", "odt", "msg"} {
		t.Run(ext, func(t *testing.T) {
			data := readerAtMSG()
			want := "subject body"
			if ext != "msg" {
				name := "word/document.xml"
				if ext == "odt" {
					name = "content.xml"
				}
				want = "maximum budget text"
				data = readerAtZIP(t, name, "<text>"+want+"</text>", 0)
			}
			e, _ := NewExtractorRegistry().GetExtractor(ext)
			text, err := e.(ReaderAtExtractor).ExtractReaderAt(bytes.NewReader(data), int64(len(data)), math.MaxInt64)
			if err != nil || text != want {
				t.Fatalf("extraction = (%q, %v), want (%q, nil)", text, err, want)
			}
		})
	}
}

func TestReaderAtExtractorEnforcesMaxExtractedBytes(t *testing.T) {
	for ext, name := range map[string]string{"docx": "word/document.xml", "odt": "content.xml"} {
		t.Run(ext, func(t *testing.T) {
			xml := "<text>" + strings.Repeat("a", 4096) + "</text>"
			data := readerAtZIP(t, name, xml, 0)
			e, _ := NewExtractorRegistry().GetExtractor(ext)
			for _, limit := range []int64{1, 4096, int64(len(xml)) - 1} {
				text, err := e.(ReaderAtExtractor).ExtractReaderAt(bytes.NewReader(data), int64(len(data)), limit)
				if text != "" || !errors.Is(err, ErrExtractionLimit) {
					t.Fatalf("limit %d: extraction = (%q, %v)", limit, text, err)
				}
			}
			for _, limit := range []int64{int64(len(xml)), 0, -1} {
				text, err := e.(ReaderAtExtractor).ExtractReaderAt(bytes.NewReader(data), int64(len(data)), limit)
				if err != nil || text != strings.Repeat("a", 4096) {
					t.Fatalf("limit %d: text length %d, error %v", limit, len(text), err)
				}
			}
		})
	}
	t.Run("pdf", func(t *testing.T) {
		text, err := (&PDFExtractor{}).ExtractReaderAt(strings.NewReader(minimalPDFWithText), int64(len(minimalPDFWithText)), 4)
		if text != "" || !errors.Is(err, ErrExtractionLimit) {
			t.Fatalf("extraction = (%q, %v)", text, err)
		}
	})
}

func TestDOCAndMSGReaderAtBoundedFallback(t *testing.T) {
	for ext, e := range map[string]ReaderAtExtractor{"doc": &DOCExtractor{}, "msg": &MSGExtractor{}} {
		t.Run(ext, func(t *testing.T) {
			data := []byte("fallback prefix " + strings.Repeat(" ", 4096-len("fallback prefix ")))
			r := &guardedReaderAt{t: t, r: bytes.NewReader(data), end: 4096}
			text, err := e.ExtractReaderAt(r, 50*1024*1024, 4096)
			if err != nil || text == "" {
				t.Fatalf("extraction = (%q, %v)", text, err)
			}
			if r.maxEnd != 4096 {
				t.Fatalf("prefix read ends at %d, want 4096", r.maxEnd)
			}
		})
	}
}

func TestReaderAtExtractorsRejectInvalidInput(t *testing.T) {
	for _, ext := range []string{"docx", "odt", "pdf", "doc", "msg"} {
		t.Run(ext, func(t *testing.T) {
			e, _ := NewExtractorRegistry().GetExtractor(ext)
			ra, ok := e.(ReaderAtExtractor)
			if !ok {
				t.Fatal("missing ReaderAtExtractor capability")
			}
			if text, err := ra.ExtractReaderAt(nil, 0, 0); text != "" || err == nil {
				t.Fatalf("nil reader: (%q, %v)", text, err)
			}
			if text, err := ra.ExtractReaderAt(bytes.NewReader(nil), -1, 0); text != "" || err == nil {
				t.Fatalf("negative size: (%q, %v)", text, err)
			}
		})
	}
}

// readerAtMSG builds a CFB v3 file with two 4096-byte text streams and an
// attachment in the final eight sectors. No mini stream is needed.
func readerAtMSG() []byte {
	const free = uint32(0xffffffff)
	const end = uint32(0xfffffffe)
	data := make([]byte, 27*512)
	put16 := binary.LittleEndian.PutUint16
	put32 := binary.LittleEndian.PutUint32
	copy(data, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	put16(data[24:], 0x3e)
	put16(data[26:], 3)
	put16(data[28:], 0xfffe)
	put16(data[30:], 9)
	put16(data[32:], 6)
	put32(data[44:], 1) // one FAT sector
	put32(data[48:], 1) // directory sector
	put32(data[56:], 4096)
	put32(data[60:], end)
	put32(data[68:], end)
	for off := 76; off < 512; off += 4 {
		put32(data[off:], free)
	}
	put32(data[76:], 0) // FAT is sector zero
	fat := data[512:1024]
	for off := 0; off < len(fat); off += 4 {
		put32(fat[off:], free)
	}
	put32(fat, 0xfffffffd)
	put32(fat[4:], end)
	for start := 2; start <= 18; start += 8 {
		for sn := start; sn < start+7; sn++ {
			put32(fat[sn*4:], uint32(sn+1))
		}
		put32(fat[(start+7)*4:], end)
	}
	entry := func(id int, name string, kind byte, start, size, left, right, child uint32) {
		d := data[1024+id*128 : 1024+(id+1)*128]
		for i, c := range name {
			put16(d[i*2:], uint16(c))
		}
		put16(d[64:], uint16((len(name)+1)*2))
		d[66], d[67] = kind, 1
		put32(d[68:], left)
		put32(d[72:], right)
		put32(d[76:], child)
		put32(d[116:], start)
		put32(d[120:], size)
	}
	entry(0, "Root Entry", 5, end, 0, free, free, 2)
	entry(1, "__substg1.0_0037001F", 2, 2, 4096, free, free, free)
	entry(2, "__substg1.0_1000001F", 2, 10, 4096, 1, 3, free)
	entry(3, "__substg1.0_37010102", 2, 18, 4096, free, free, free)
	for start, text := range map[int]string{3 * 512: "subject", 11 * 512: "body"} {
		b := data[start : start+4096]
		put16(b, 0xfeff)
		for off := 2; off < len(b); off += 2 {
			put16(b[off:], ' ')
		}
		for i, c := range text {
			put16(b[2+i*2:], uint16(c))
		}
	}
	return data
}

// utf16LE encodes s as BOM-less UTF-16LE.
func utf16LE(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

// cfbWithRootStreams reuses the readerAtMSG layout, renaming its two
// 4096-byte root streams and zero-filling them after each payload.
func cfbWithRootStreams(names [2]string, payloads [2][]byte) []byte {
	data := readerAtMSG()
	for i, name := range names {
		d := data[1024+(i+1)*128 : 1024+(i+2)*128]
		clear(d[:64])
		for j, c := range name {
			binary.LittleEndian.PutUint16(d[j*2:], uint16(c))
		}
		binary.LittleEndian.PutUint16(d[64:], uint16((len(name)+1)*2))
		stream := data[(3+8*i)*512 : (11+8*i)*512]
		clear(stream)
		copy(stream, payloads[i])
	}
	return data
}

// ansiMSG holds space-padded ANSI subject and body streams.
func ansiMSG() []byte {
	return cfbWithRootStreams([2]string{"__substg1.0_0037001E", "__substg1.0_1000001E"},
		[2][]byte{[]byte("subject" + strings.Repeat(" ", 4089)), []byte("body" + strings.Repeat(" ", 4092))})
}

func TestMSGExtractorDecodesStringStreamsByPropertyType(t *testing.T) {
	hiddenHTML := utf16LE("<html><style>cssneedle</style><script>jsneedle()</script><body>" + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "</body></html>")
	for _, tc := range []struct {
		name    string
		data    []byte
		want    string
		notWant string
	}{
		{"ANSI 001E", ansiMSG(), "subject body", ""},
		{"non-CFB 8-bit fallback", []byte("arithmetic exception, numeric overflow"), "arithmetic exception, numeric overflow", ""},
		{"binary UTF-16LE HTML drops hidden blocks", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{hiddenHTML, nil}), "\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", "cssneedle jsneedle"},
		{"non-CFB UTF-16LE HTML drops hidden blocks", hiddenHTML, "\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", "cssneedle jsneedle"},
		{"Unicode 001F without BOM", cfbWithRootStreams(
			[2]string{"__substg1.0_0037001F", "__substg1.0_1000001F"},
			[2][]byte{utf16LE(strings.Repeat("Тема ", 409)), utf16LE("body")}), "Тема Тема", ""},
		{"binary HTML without BOM and predominantly non-Latin body", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{utf16LE(`<html><body><p class="MsoNormal">` + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "</p></body></html>"), nil}), "\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", "MsoNormal"},
		{"binary UTF-16LE HTML isolates stray brackets between readings", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{utf16LE("<html><body><p>report > limit</p>" + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "\u503c"), nil}), "report limit \u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", ""},
		{"non-CFB UTF-16LE HTML strips attributes", utf16LE(
			`<html><body><p class="MsoNormal">` + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "</p></body></html>"), "\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", "MsoNormal"},
		{"binary CP1252 HTML preserves entity-encoded brackets", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte("<html><body>Caf\xe9 <p>From: John &lt;john@example.com&gt; Q3 &lt; Q4 forecast revenue &gt; target</p></body></html>"), nil}), "John <john@example.com> Q3 < Q4 forecast revenue > target", ""},
		{"binary non-Latin UTF-16LE HTML preserves entity-encoded brackets", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{utf16LE("<html><body>" + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "<p>From: John &lt;john@example.com&gt; Q3 &lt; Q4 forecast revenue &gt; target</p></body></html>"), nil}), "John <john@example.com> Q3 < Q4 forecast revenue > target", ""},
		{"binary HTML without BOM and predominantly Cyrillic body", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{utf16LE("<html><body>" + strings.Repeat("Отчёт о продажах", 30) + "</body></html>"), nil}), "Отчёт о продажах", ""},
		{"binary UTF-8 HTML preserves non-ASCII text and strips attributes", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte(`<html><body><p class="MsoNormal">café 東京都の報告書 quarterly</p></body></html>`), nil}), "café 東京都の報告書 quarterly", "MsoNormal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&MSGExtractor{}).ExtractText(tc.data)
			if err != nil || !strings.Contains(got, tc.want) {
				t.Errorf("extraction = (%q, %v), want containing %q", got, err, tc.want)
			}
			for _, unwanted := range strings.Fields(tc.notWant) {
				if strings.Contains(got, unwanted) {
					t.Errorf("extraction = %q, must not contain %q", got, unwanted)
				}
			}
		})
	}
}

// docWithWordStream places text inside a zero-padded WordDocument stream
// between binary noise that includes stray '<' and '>' bytes.
func docWithWordStream(text []byte) []byte {
	stream := make([]byte, 4096)
	copy(stream[1024:], "\x01<\x02")
	copy(stream[1536:], text)
	copy(stream[3072:], "\x05>\x06")
	return cfbWithRootStreams([2]string{"WordDocument", "1Table"}, [2][]byte{stream, nil})
}

func TestDOCExtractorReadsEightBitAndUTF16LEStreams(t *testing.T) {
	for _, tc := range []struct {
		name string
		text []byte
		want string
	}{
		{"UTF-16LE ASCII", utf16LE("subject body"), "subject body"},
		{"8-bit", []byte("subject body"), "subject body"},
		{"UTF-16LE Cyrillic", utf16LE("Привет мир документ"), "Привет мир документ"},
		{"UTF-16LE Latin-1", utf16LE("Résumé für Ärzte"), "Résumé für Ärzte"},
		{"UTF-16LE CJK", utf16LE("東京都の報告書"), "東京都の報告書"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&DOCExtractor{}).ExtractText(docWithWordStream(tc.text))
			if err != nil || !strings.Contains(got, tc.want) || strings.ContainsAny(got, "<>") {
				t.Fatalf("extraction = (%q, %v), want containing %q without angle brackets", got, err, tc.want)
			}
		})
	}
}

func TestTryDecodeUTF16BestEffortRequiresBOMOrDominantNULLane(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
		ok   bool
	}{
		{"BOM LE", append([]byte{0xff, 0xfe}, utf16LE("hi there")...), "hi there", true},
		{"BOM BE", []byte{0xfe, 0xff, 0, 'h', 0, 'i'}, "hi", true},
		{"BOM-less LE", utf16LE("hello world"), "hello world", true},
		{"BOM-less BE", []byte{0, 'h', 0, 'e', 0, 'l', 0, 'l', 0, 'o'}, "hello", true},
		{"binary-prefixed bracketed LE", append([]byte{1, 2}, utf16LE("<"+strings.Repeat("subject body ", 30)+">")...), "ȁ<" + strings.Repeat("subject body ", 30) + ">", true},
		{"8-bit text", []byte("arithmetic exception"), "", false},
		{"zero-padded 8-bit", []byte(strings.Repeat("\x00", 64) + "subject body" + strings.Repeat("\x00", 64)), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tryDecodeUTF16BestEffort(tc.data)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("decode = (%q, %t), want (%q, %t)", got, ok, tc.want, tc.ok)
			}
			if ok {
				if got := decodeLegacyStreamText(tc.data, false); got != tc.want {
					t.Errorf("nonbinary legacy decode = %q, want %q", got, tc.want)
				}
				want := strings.TrimSpace(whitespaceRegex.ReplaceAllString(strings.NewReplacer("<", " ", ">", " ").Replace(tc.want), " "))
				got := decodeLegacyStreamText(tc.data, true)
				if got != want || strings.ContainsAny(got, "<>") {
					t.Errorf("binary legacy decode = %q, want %q without angle brackets", got, want)
				}
				if cleaned := cleanStage3Content(got, false).text; cleaned != want {
					t.Errorf("Stage 3 cleaned text = %q, want %q", cleaned, want)
				}
			}
		})
	}
}

func TestMSGReaderAtSkipsAttachmentsAndBoundsCumulativeStreams(t *testing.T) {
	data := readerAtMSG()
	for _, limit := range []int64{8192, 8191, 4096, 4095} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			// Any attachment read is a test failure, even if later discarded.
			r := &guardedReaderAt{t: t, r: bytes.NewReader(data), end: 19 * 512}
			text, err := (&MSGExtractor{}).ExtractReaderAt(r, int64(len(data)), limit)
			if limit == 8192 {
				if err != nil || text != "subject body" {
					t.Fatalf("extraction = (%q, %v)", text, err)
				}
			} else if text != "" || !errors.Is(err, ErrExtractionLimit) {
				t.Fatalf("extraction = (%q, %v), want extraction limit", text, err)
			}
		})
	}
}

// pdfWithPageStreams generates real xref offsets for arbitrary page operators.
func pdfWithPageStreams(streams ...string) []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	var kids strings.Builder
	for i, stream := range streams {
		pageID := 4 + 2*i
		fmt.Fprintf(&kids, "%d 0 R ", pageID)
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", pageID+1),
			fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), len(streams))
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f\n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n\n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

func TestPDFReaderAtPageTextBudgets(t *testing.T) {
	const text = "BT /F1 12 Tf 72 720 Td (abcd) Tj ET\n"
	for _, tc := range []struct {
		name    string
		streams []string
		limit   int64
		want    string
	}{
		{"single raw bytes", []string{text}, 3, ""},
		{"single page newline", []string{text}, 4, ""},
		{"single exact", []string{text}, 5, "abcd"},
		{"space separator", []string{"BT /F1 12 Tf 72 720 Td (ab) Tj 100 0 Td (cd) Tj ET\n"}, 4, ""},
		{"line separator", []string{"BT /F1 12 Tf 72 720 Td (ab) Tj 0 -20 Td (cd) Tj ET\n"}, 4, ""},
		{"multiple raw bytes", []string{text, text}, 8, ""},
		{"multiple exhausted", []string{text, text}, 5, ""},
		{"multiple exact", []string{text, text}, 10, "abcd\nabcd"},
		{"empty page after exhausted", []string{text, ""}, 5, "abcd"},
		{"whitespace page after exhausted", []string{text, "BT /F1 12 Tf 72 720 Td (   ) Tj ET\n"}, 5, "abcd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := pdfWithPageStreams(tc.streams...)
			engine := &SearchEngine{}
			_, _, before := engine.GetPDFStatsDetailed()
			got, err := (&PDFExtractor{}).ExtractReaderAt(bytes.NewReader(data), int64(len(data)), tc.limit)
			_, _, after := engine.GetPDFStatsDetailed()
			var wantTruncated int64
			if tc.want == "" {
				// Each completed nonempty fixture page consumes five bytes.
				wantTruncated = int64(len(tc.streams)) - tc.limit/5
			}
			if after-before != wantTruncated {
				t.Errorf("truncated pages delta = %d, want %d", after-before, wantTruncated)
			}
			if tc.want == "" {
				if got != "" || !errors.Is(err, ErrExtractionLimit) {
					t.Fatalf("extraction = (%q, %v), want extraction limit", got, err)
				}
				if !strings.Contains(err.Error(), fmt.Sprintf("(%d bytes)", tc.limit)) {
					t.Fatalf("error must report document budget: %v", err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("extraction = (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestPDFReaderAtPlainTextFallbackBudgets(t *testing.T) {
	// An unmatched Q panics in Content's graphics stack, but is ignored by
	// GetPlainText. This forces the real fallback without test-only hooks.
	const fallback = "Q BT /F1 12 Tf (abcd) Tj ET\n"
	// Invalid Tj operands also fail GetPlainText. Eager document extraction
	// would reach this page and discard all text instead of reporting the limit.
	const broken = "Q BT Tj ET\n"
	for _, tc := range []struct {
		name    string
		streams []string
		limit   int64
		want    string
	}{
		{"single limit before broken page", []string{fallback, broken}, 4, ""},
		{"cumulative limit before broken page", []string{fallback, fallback, broken}, 9, ""},
		{"exact single", []string{fallback}, 5, "abcd"},
		{"exact cumulative", []string{fallback, fallback}, 10, "abcd\nabcd"},
		{"retain preceding text on page error", []string{fallback, broken}, 10, "abcd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := pdfWithPageStreams(tc.streams...)
			engine := &SearchEngine{}
			_, _, before := engine.GetPDFStatsDetailed()
			got, err := (&PDFExtractor{}).ExtractReaderAt(bytes.NewReader(data), int64(len(data)), tc.limit)
			_, _, after := engine.GetPDFStatsDetailed()
			var wantTruncated int64
			if tc.want == "" {
				// Each completed nonempty fixture page consumes five bytes.
				wantTruncated = int64(len(tc.streams)) - tc.limit/5
			}
			if after-before != wantTruncated {
				t.Errorf("truncated pages delta = %d, want %d", after-before, wantTruncated)
			}
			if tc.want == "" {
				if got != "" || !errors.Is(err, ErrExtractionLimit) {
					t.Fatalf("extraction = (%q, %v), want extraction limit", got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("extraction = (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
}

// forbiddenSectorReader rejects reads of nested payload, while permitting the
// directory sector after it. Discarding a nested read later is still a failure.
type forbiddenSectorReader struct {
	t *testing.T
	*bytes.Reader
}

func (r *forbiddenSectorReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 27*512 && off+int64(len(p)) > 19*512 {
		r.t.Errorf("read nested MSG payload at [%d, %d)", off, off+int64(len(p)))
		return 0, fmt.Errorf("nested payload read")
	}
	return r.Reader.ReadAt(p, off)
}

func TestMSGReaderAtSkipsNestedAllowlistedStreams(t *testing.T) {
	for _, name := range []string{
		"__substg1.0_0037001F", "__substg1.0_0037001E",
		"__substg1.0_1000001F", "__substg1.0_1000001E",
		"__substg1.0_1013001F", "__substg1.0_1013001E", "__substg1.0_10130102",
	} {
		t.Run(name, func(t *testing.T) {
			data := append(readerAtMSG(), make([]byte, 512)...)
			put16 := binary.LittleEndian.PutUint16
			put32 := binary.LittleEndian.PutUint32
			// Extend the directory chain to sector 26, and replace the old
			// attachment stream with a storage whose child is entry 4.
			put32(data[512+4:], 26)
			put32(data[512+26*4:], 0xfffffffe)
			storage := data[1024+3*128 : 1024+4*128]
			storage[66] = 1
			put32(storage[76:], 4)
			put32(storage[116:], 0xfffffffe)
			put32(storage[120:], 0)
			nested := data[27*512 : 27*512+128]
			for i, c := range name {
				put16(nested[i*2:], uint16(c))
			}
			put16(nested[64:], uint16((len(name)+1)*2))
			nested[66], nested[67] = 2, 1
			for _, off := range []int{68, 72, 76} {
				put32(nested[off:], 0xffffffff)
			}
			put32(nested[116:], 18)
			put32(nested[120:], 4096)
			// Distinct nested text would overwrite root properties if read.
			copy(data[19*512:27*512], data[3*512:11*512])
			put16(data[19*512+2:], 'X')
			for _, limit := range []int64{8192, 12288} {
				r := &forbiddenSectorReader{t: t, Reader: bytes.NewReader(data)}
				got, err := (&MSGExtractor{}).ExtractReaderAt(r, int64(len(data)), limit)
				if err != nil || got != "subject body" {
					t.Fatalf("limit %d: extraction = (%q, %v)", limit, got, err)
				}
			}
		})
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

// pdfBoundaryReader cancels the real contextReaderAt immediately before a
// selected page-boundary probe, independently of the PDF library's read cache.
type pdfBoundaryReader struct {
	r              contextReaderAt
	stop           func()
	stopAt         int
	probes         int
	readsAfterStop int
}

func (r *pdfBoundaryReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		r.probes++
		if r.probes == r.stopAt {
			r.stop()
		}
	} else if r.r.ctx.Err() != nil {
		r.readsAfterStop++
	}
	return r.r.ReadAt(p, off)
}

func TestPDFReaderAtPageBoundaryCancellation(t *testing.T) {
	const pages = 4
	// Empty pages exercise both positioned extraction and plain-text fallback.
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R 6 0 R] /Count 4 >>",
	}
	for i := 0; i < pages; i++ {
		objects = append(objects, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>")
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	var offsets []int
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f\n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n\n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	data := b.String()
	for _, phase := range []struct {
		name   string
		stopAt int
	}{
		{"positioned", 2}, {"fallback", pages + 2},
	} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired=%v", phase.name, expired), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r := &pdfBoundaryReader{r: contextReaderAt{ctx, strings.NewReader(data)}, stopAt: phase.stopAt, stop: cancel}
				want := context.Canceled
				if expired {
					want = context.DeadlineExceeded
					r.stop = func() {
						deadlineCtx, release := context.WithDeadline(ctx, time.Now().Add(-time.Second))
						defer release()
						r.r.ctx = deadlineCtx
					}
				}
				text, err := (&PDFExtractor{}).ExtractReaderAt(r, int64(len(data)), 0)
				if text != "" || !errors.Is(err, want) {
					t.Fatalf("extraction = (%q, %v), want empty text and %v", text, err, want)
				}
				if r.probes != phase.stopAt || r.readsAfterStop != 0 {
					t.Fatalf("probes = %d, reads after cancellation = %d; want %d and 0", r.probes, r.readsAfterStop, phase.stopAt)
				}
			})
		}
	}
	t.Run("EOF probe is harmless", func(t *testing.T) {
		r := &pdfEOFProbeReader{ReaderAt: strings.NewReader(data)}
		text, err := (&PDFExtractor{}).ExtractReaderAt(r, int64(len(data)), 0)
		if text != "" || err != nil || r.probes != 2*pages {
			t.Fatalf("extraction = (%q, %v), probes = %d; want empty, nil, %d", text, err, r.probes, 2*pages)
		}
	})
}

type pdfEOFProbeReader struct {
	io.ReaderAt
	probes int
}

func (r *pdfEOFProbeReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		r.probes++
		return 0, io.EOF
	}
	return r.ReaderAt.ReadAt(p, off)
}

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
