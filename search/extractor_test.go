package search

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5"
	"crypto/rc4"
	"encoding/ascii85"
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
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"golang.org/x/text/encoding/charmap"
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

func TestMarkupExtractorsStripQuotedTagsAndDeclarations(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`<p class="x">alpha</p>`, "alpha"},
		{`<a title="a<b">alpha</a>`, "alpha"},
		{`<div data-x="a>b">alpha</div>`, "alpha"},
		{`<p class="x>alpha</p>`, "alpha"},
		{`<img alt=O'Brien>Name<p>It's ok</p>`, "Name It's ok"},
		{`<img alt=O'Brien title="a>b">alpha`, "alpha"},
		{`<img alt=O'Brien title='a<b'>alpha`, "alpha"},
		{`<p class="x>alpha</p><p class="y">beta</p>`, "alpha beta"},
		{`<p class='x>alpha</p><p class='y'>beta</p>`, "alpha beta"},
		{`<p class="x>alpha</p>" >tail`, "tail"}, // Balanced attribute, not malformed recovery.
		{`<p class="x>alpha</p> >tail`, "alpha >tail"},
		{`<p class='x>alpha</p>tail`, "alpha tail"},
		{`<img src="secret" alt="load </b> value">alpha`, "alpha"},
		{`<img src='secret' alt='load </b> value'>alpha`, "alpha"},
		{`<?audit secret < 80% ?>alpha`, "alpha"},
		{`<?audit secret </x ?>alpha`, "alpha"},
		{`<?audit secret?>alpha`, "alpha"},
		{"<?audit secret <\n80% ?>alpha", "alpha"},
		{`<?audit secret`, "<?audit secret"},
		{`<?audit secret>alpha`, "alpha"},
		{`<?a>one<?b>two<?c?>three`, "one two three"},
		{`<p>Hi<?xml:namespace prefix = o ns="urn:schemas-microsoft-com:office:office" /><o:p></o:p></p><p>budget visible</p><?php echo 1 ?>tail`, "Hi budget visible tail"},
		{`<?audit secret>alpha<b>x</b>?>tail`, "alpha x ?>tail"},
		{`<?audit > < 80% ?>alpha`, "alpha"},
		{`<?audit < nested ?>alpha`, "alpha"},
		{`<?xml:namespace prefix = o ns="urn:schemas-microsoft-com:office:office" />budget`, "budget"},
		{`<?no closer at all`, "<?no closer at all"},
		{`<?php if ($a > 1) ?>alpha`, "alpha"},
		{`<?audit < nested>alpha`, "alpha"},
		{"<?audit secret\nvalue>alpha", "alpha"},
		{"<?audit secret>alpha\n?>tail", "tail"},
		{"<?audit secret>alpha\n<b>x</b>?>tail", "alpha x ?>tail"},
		{`<?audit secret>alpha<br>?>tail`, "alpha ?>tail"},
		{`<?audit secret>alpha</p>?>tail`, "alpha ?>tail"},
		{`<?audit secret>alpha<!-- c -->?>tail`, "alpha ?>tail"},
		{`<?first>alpha <?second>tail <?unclosed`, "alpha tail <?unclosed"},
		{`<title>a <!-- b</title>alpha<!-- x -->tail`, "a <!-- b alpha tail"},
		{`<textarea>a <!-- b</textarea>alpha<!-- x -->tail`, "a <!-- b alpha tail"},
		{`<title>heading</title>alpha`, "heading alpha"},
		{`<script>secret</script foo>alpha`, "alpha"},
		{`<script>secret</script >alpha`, "alpha"},
		{`<script>secret</script/>alpha`, "alpha"},
		{`<style>secret</style foo>alpha`, "alpha"},
		{`<script/>alpha<script>secret</script>tail`, "alpha tail"},
		{`<style type="text/css"/>alpha<style>secret</style>tail`, "alpha tail"},
		{`<img src="secret" alt="load <80%">alpha`, "alpha"},
		{"<img src=\"secret\" alt=\n\"load <80%\">alpha", "alpha"},
		{"<img src='secret' alt=\r\n	\f 'load <80%'>alpha", "alpha"},
		{`<img src="secret" alt="load </ 80%">alpha`, "alpha"},
		{`<img src="secret" alt="<<">alpha`, "alpha"},
		{`<img src='secret' alt='<< / </'>alpha`, "alpha"},
		{`<!DOCTYPE secret [<!-- don't -->]><secret>alpha</secret>`, "alpha"},
		{`<img alt="<!--">alpha<!-- footer -->tail`, "alpha tail"},
		{`<!-- <script> -->alpha<script>hidden</script>tail`, "alpha tail"},
		{`<![CDATA[<style>]]>alpha<style>hidden</style>tail`, "alpha tail"},
		{`<script>"<style>"</script>alpha<style>hidden</style>tail`, "alpha tail"},
		{`<SCRIPT title="a>b">"<!--"</SCRIPT>alpha<!-- footer -->tail`, "alpha tail"},
		{`<scripture>alpha</scripture>tail`, "alpha tail"},
		{"<img src='secret'\nalt='load <80% >20%'>alpha", "alpha"},
		{"<img alt=\"load <80%\n>20%\">alpha", "alpha"},
		{"<!-- secret < 80%\ninternal note -->alpha", "alpha"},
		{"<!-- secret\ninternal note -->alpha", "alpha"},
		{"<![CDATA[secret < 80%\ninternal > note]]>alpha", "alpha"},
		{"<!DOCTYPE root [\n<!ENTITY note 'secret < 80% >20%'>\n]>alpha", "alpha"},
		{`<!DOCTYPE root [<!ENTITY note "[ ] ' > <">]>alpha`, "alpha"},
		{"<!a [\n<b>alpha</b>\nbeta", "<!a [ alpha beta"},
		{"<!DOCTYPE x [\n<b>alpha</b>\nbeta", "<!DOCTYPE x [ alpha beta"},
	} {
		for _, extractor := range []Extractor{&HTMLExtractor{}, &XMLExtractor{}} {
			got, err := extractor.ExtractText([]byte(tc.raw))
			if err != nil || strings.Join(strings.Fields(got), " ") != tc.want {
				t.Errorf("%T(%q) = (%q, %v), want %q", extractor, tc.raw, got, err, tc.want)
			}
		}
		if got := strings.Join(strings.Fields(stripHTMLTags(tc.raw)), " "); got != tc.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		// Legacy reading cleanup additionally blanks stray angle brackets.
		want := strings.Join(strings.Fields(strings.NewReplacer("<", " ", ">", " ").Replace(tc.want)), " ")
		if got := strings.Join(strings.Fields(stripReadingMarkup(tc.raw)), " "); got != want {
			t.Errorf("stripReadingMarkup(%q) = %q, want %q", tc.raw, got, want)
		}
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

func TestDOCReaderAtBoundedFallback(t *testing.T) {
	e := &DOCExtractor{}
	for _, limit := range []int64{4096, 4 * 1024 * 1024} {
		cap := min(limit, 2*1024*1024)
		data := []byte("fallback prefix " + strings.Repeat(" ", int(cap)-len("fallback prefix ")) + "XYZ")
		r := &guardedReaderAt{t: t, r: bytes.NewReader(data), end: cap}
		text, err := e.ExtractReaderAt(r, 50*1024*1024, limit)
		if err != nil || text != "fallback prefix" {
			t.Fatalf("limit %d: extraction = (%q, %v)", limit, text, err)
		}
		if r.maxEnd != cap {
			t.Fatalf("prefix read ends at %d, want %d", r.maxEnd, cap)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	text, err := e.ExtractReaderAt(contextReaderAt{ctx, strings.NewReader("fallback prefix")}, 15, 4)
	if text != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled extraction = (%q, %v)", text, err)
	}
}

func TestMSGExtractorRequiresCFBContainer(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"plain text", []byte("arithmetic exception, numeric overflow")},
		{"UTF-8 Tcl catalog", []byte("namespace eval ::tk {\n	::msgcat::mcset de \"&Open...\" \"\u00d6&ffnen...\"\n}\n")},
		{"binary catalog", binaryCatalogMSG},
		{"sparse NUL catalog", sparseNULCatalogMSG},
		{"UTF-16LE HTML", chineseHTMLUTF16LE},
		{"large text", []byte(strings.Repeat("fallback prefix ", 4096))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &guardedReaderAt{t: t, r: bytes.NewReader(tc.data), end: 512}
			text, err := (&MSGExtractor{}).ExtractReaderAt(r, 50*1024*1024, 0)
			if text != "" || err == nil || !strings.HasPrefix(err.Error(), "failed to parse MSG: ") {
				t.Fatalf("extraction = (%q, %v), want empty text and MSG parse error", text, err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"no MAPI text streams", cfbWithRootStreams([2]string{"__nameid_version1.0", "__substg1.0_001A001E"}, [2][]byte{nil, []byte("IPM.Note")}), ""},
		{"NUL-terminated empty strings", cfbWithRootStreams([2]string{"__substg1.0_0037001E", "__substg1.0_1000001F"}, [2][]byte{{0}, {0, 0}}), ""},
		{"NUL-only plain body uses Unicode HTML", cfbWithRootStreams(
			[2]string{"__substg1.0_1000001F", "__substg1.0_1013001F"},
			[2][]byte{{0, 0}, utf16LE("<p>HTML body</p>\x00")}), "HTML body"},
		{"NUL and whitespace plain body uses ANSI HTML", cfbWithRootStreams(
			[2]string{"__substg1.0_1000001E", "__substg1.0_1013001E"},
			[2][]byte{[]byte("\x00 	\x00"), []byte("<p>HTML body</p>\x00")}), "HTML body"},
		{"plain body wins over HTML", cfbWithRootStreams(
			[2]string{"__substg1.0_1000001F", "__substg1.0_1013001F"},
			[2][]byte{utf16LE("plain body\x00"), utf16LE("<p>HTML body</p>")}), "plain body"},
		{"subject trims NUL and whitespace", cfbWithRootStreams(
			[2]string{"__substg1.0_0037001F", "__substg1.0_1000001E"},
			[2][]byte{utf16LE("\x00 	 subject \x00 \x00"), nil}), "subject"},
		{"NUL-only Unicode HTML is empty", cfbWithRootStreams(
			[2]string{"__substg1.0_1000001F", "__substg1.0_1013001F"},
			[2][]byte{{0, 0}, {0, 0}}), ""},
		{"NUL and whitespace ANSI HTML is empty", cfbWithRootStreams(
			[2]string{"__substg1.0_1013001E", "unused"},
			[2][]byte{[]byte("\x00 	\x00"), nil}), ""},
		{"NUL-only binary HTML is empty", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{{0, 0}, nil}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := (&MSGExtractor{}).ExtractText(tc.data)
			if text != tc.want || err != nil {
				t.Fatalf("extraction = (%q, %v), want (%q, nil)", text, err, tc.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := readerAtMSG()
	text, err := (&MSGExtractor{}).ExtractReaderAt(contextReaderAt{ctx, bytes.NewReader(data)}, int64(len(data)), 0)
	// mscfb stringifies read errors rather than unwrapping them.
	if text != "" || err == nil {
		t.Fatalf("canceled extraction = (%q, %v), want empty text and error", text, err)
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

// cp1252MSG holds space-padded Windows-1252 ANSI subject and body streams.
func cp1252MSG() []byte {
	return cfbWithRootStreams([2]string{"__substg1.0_0037001E", "__substg1.0_1000001E"},
		[2][]byte{[]byte("Men\xfa caf\xe9" + strings.Repeat(" ", 4087)), []byte("Le caf\xe9 cr\xe8me ordered" + strings.Repeat(" ", 4075))})
}

var binaryCatalogMSG = []byte("\x01\x01\x00\x04\x00\x00arithmetic exception, numeric overflow\x00\x00\x02\x00\x00\x00invalid database key\x00\x00")

var sparseNULCatalogMSG = []byte("arithmetic overflow\x00invalid database key\x00")

var chineseHTMLUTF16LE = utf16LE("<html><body><p>" + strings.Repeat("\u4e00\u5b63\u5ea6\u62a5\u544a", 30) + "</p></body></html>")

func TestMSGExtractorDecodesStringStreamsByPropertyType(t *testing.T) {
	hiddenHTML := utf16LE("<html><style>cssneedle</style><script>jsneedle()</script><body>" + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "</body></html>")
	for _, tc := range []struct {
		name    string
		data    []byte
		want    string
		notWant string
	}{
		{"ANSI 001E", ansiMSG(), "subject body", ""},
		{"ANSI 001E CP1252 decodes to UTF-8", cp1252MSG(), "Menú café Le café crème ordered", ""},
		{"ANSI 001E UTF-8 is not decoded twice", cfbWithRootStreams(
			[2]string{"__substg1.0_0037001E", "__substg1.0_1000001E"},
			[2][]byte{[]byte("Le café crème" + strings.Repeat(" ", 4081)), []byte("body" + strings.Repeat(" ", 4092))}), "Le café crème", "cafÃ©"},
		{"binary CP1252 HTML decodes 8-bit text", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte("<html><body><p>Le caf\xe9 cr\xe8me au lait</p></body></html>"), nil}), "Le café crème au lait", ""},
		{"binary CP1252 HTML excludes UTF-16LE mojibake", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte("<html><body><p>cp1252htmlmarker caf\xe9 text \x93quoted\x94</p></body></html>"), nil}), "cp1252htmlmarker caf\u00e9 text \u201cquoted\u201d", "\u683c\u6d74\u3e6c\u623c\u646f\u3e79"},
		{"binary 0102 sparse NUL text", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{sparseNULCatalogMSG, nil}), "arithmetic overflow", "\u7261\u7469\u6d68\u7465\u6369"},
		{"binary UTF-16LE HTML with low-byte NULs", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{chineseHTMLUTF16LE, nil}), "\u4e00\u5b63\u5ea6\u62a5\u544a", ""},
		{"binary UTF-16LE HTML drops hidden blocks", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{hiddenHTML, nil}), "\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", "cssneedle jsneedle"},
		{"Unicode 001F without BOM", cfbWithRootStreams(
			[2]string{"__substg1.0_0037001F", "__substg1.0_1000001F"},
			[2][]byte{utf16LE(strings.Repeat("Тема ", 409)), utf16LE("body")}), "Тема Тема", ""},
		{"binary HTML without BOM and predominantly non-Latin body", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{utf16LE(`<html><body><p class="MsoNormal">` + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "</p></body></html>"), nil}), "\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", "MsoNormal"},
		{"binary UTF-16LE HTML isolates stray brackets between readings", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{utf16LE("<html><body><p>report > limit</p>" + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "\u503c"), nil}), "report limit \u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", ""},
		{"binary CP1252 HTML preserves entity-encoded brackets", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte("<html><body>Caf\xe9 <p>From: John &lt;john@example.com&gt; Q3 &lt; Q4 forecast revenue &gt; target</p></body></html>"), nil}), "John <john@example.com> Q3 < Q4 forecast revenue > target", ""},
		{"binary non-Latin UTF-16LE HTML preserves entity-encoded brackets", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{utf16LE("<html><body>" + strings.Repeat("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", 30) + "<p>From: John &lt;john@example.com&gt; Q3 &lt; Q4 forecast revenue &gt; target</p></body></html>"), nil}), "John <john@example.com> Q3 < Q4 forecast revenue > target", ""},
		{"binary CP1252 HTML strips multi-line tags and keeps text between stray comparison brackets", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte("<html><body>Caf\xe9 <p class=MsoNormal\nstyle='color:red'>load <80% keeps\nwarm cache until >90%</p></body></html>"), nil}), "keeps warm cache until", "MsoNormal"},
		{"binary HTML without BOM and predominantly Cyrillic body", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{utf16LE("<html><body>" + strings.Repeat("Отчёт о продажах", 30) + "</body></html>"), nil}), "Отчёт о продажах", ""},
		{"binary CP1252 HTML strips quoted attributes after newline", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte("Caf\xe9 <img src=\"secret\" alt=\n\"load <80%\">alpha"), nil}), "alpha", "secret"},
		{"binary CP1252 HTML strips angle sequences in attributes", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte("Caf\xe9 <img src=\"secret\" alt=\"<< </ 80%\">alpha"), nil}), "alpha", "secret"},
		{"binary CP1252 HTML strips ordinary attributes", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte("Caf\xe9 <img src=\"secret\" alt=\"load\">alpha"), nil}), "alpha", "secret"},
		{"binary UTF-8 HTML preserves non-ASCII text and strips attributes", cfbWithRootStreams(
			[2]string{"__substg1.0_10130102", "unused"},
			[2][]byte{[]byte(`<html><body><p class="MsoNormal">café 東京都の報告書 quarterly</p></body></html>`), nil}), "café 東京都の報告書 quarterly", "MsoNormal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&MSGExtractor{}).ExtractReaderAt(bytes.NewReader(tc.data), int64(len(tc.data)), 0)
			if err != nil || !strings.Contains(got, tc.want) {
				t.Errorf("extraction = (%q, %v), want containing %q", got, err, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("extraction = %q, want valid UTF-8", got)
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

// wordStreamsWithPieces builds a Word 97 FIB and CLX pointing to repeated pieces.
func wordStreamsWithPieces(text string, unicode bool, copies int) [2][]byte {
	word, table := make([]byte, 4096), make([]byte, 4096)
	put16, put32 := binary.LittleEndian.PutUint16, binary.LittleEndian.PutUint32
	put16(word, 0xa5ec)
	put16(word[2:], 0x00c1)
	put16(word[0x0a:], 0x0200)
	put16(word[32:], 14)
	put16(word[62:], 22)
	put16(word[152:], 93)
	put32(word[154+33*8+4:], uint32(5+4+12*copies))
	payload, err := charmap.Windows1252.NewEncoder().Bytes([]byte(text))
	fc, count := uint32(2048|0x40000000), len(payload)
	if unicode {
		payload = utf16LE(text)
		fc, count = 1024, len(payload)/2
	} else if err != nil {
		panic(err)
	}
	copy(word[1024:], payload)
	table[0] = 0x02
	put32(table[1:], uint32(4+12*copies))
	for i := 0; i <= copies; i++ {
		put32(table[5+4*i:], uint32(i*count))
	}
	for i := 0; i < copies; i++ {
		put32(table[5+4*(copies+1)+8*i+2:], fc)
	}
	return [2][]byte{word, table}
}

func wordDocWithText(text string, unicode bool) []byte {
	return cfbWithRootStreams([2]string{"WordDocument", "1Table"}, wordStreamsWithPieces(text, unicode, 1))
}

func TestDOCExtractorReadsEightBitAndUTF16LEStreams(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"8-bit salvage without FIB", docWithWordStream([]byte("subject body")), "subject body"},
		{"sparse NUL catalog without FIB", cfbWithRootStreams([2]string{"WordDocument", "1Table"},
			[2][]byte{sparseNULCatalogMSG, nil}), "arithmetic overflow invalid database key"},
		{"UTF-16LE HTML with low-byte NULs without FIB", docWithWordStream(chineseHTMLUTF16LE), "\u4e00\u5b63\u5ea6\u62a5\u544a"},
		{"dense UTF-16LE without FIB", cfbWithRootStreams([2]string{"WordDocument", "1Table"},
			[2][]byte{utf16LE(strings.Repeat("subject body ", 157)), nil}), "subject body subject body"},
		{"UTF-16LE ASCII piece", wordDocWithText("subject body", true), "subject body"},
		{"UTF-16LE Cyrillic piece", wordDocWithText("\u041f\u0440\u0438\u0432\u0435\u0442 \u043c\u0438\u0440 \u0434\u043e\u043a\u0443\u043c\u0435\u043d\u0442", true), "\u041f\u0440\u0438\u0432\u0435\u0442 \u043c\u0438\u0440 \u0434\u043e\u043a\u0443\u043c\u0435\u043d\u0442"},
		{"UTF-16LE Latin-1 piece", wordDocWithText("R\u00e9sum\u00e9 f\u00fcr \u00c4rzte", true), "R\u00e9sum\u00e9 f\u00fcr \u00c4rzte"},
		{"UTF-16LE CJK piece", wordDocWithText("\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8", true), "\u6771\u4eac\u90fd\u306e\u5831\u544a\u66f8"},
		{"compressed Windows-1252 piece", wordDocWithText("R\u00e9sum\u00e9 caf\u00e9 a < b", false), "R\u00e9sum\u00e9 caf\u00e9 a b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&DOCExtractor{}).ExtractText(tc.data)
			if err != nil || !strings.Contains(got, tc.want) || strings.ContainsAny(got, "<>") {
				t.Fatalf("extraction = (%q, %v), want containing %q without angle brackets", got, err, tc.want)
			}
			isHighRune := func(r rune) bool { return r >= 0x3000 }
			if strings.IndexFunc(tc.want, isHighRune) < 0 && strings.IndexFunc(got, isHighRune) >= 0 {
				t.Errorf("extraction = %q, must not contain spurious high runes", got)
			}
		})
	}
}

func TestWordPieceTextCapsOverlappingPieces(t *testing.T) {
	for _, tc := range []struct {
		name, first, second, want string
		gap                       int
		compressed                bool
	}{
		{"split pair adjacent", "A\x00\x3d\xd8", "\x00\xdeB\x00", "A😀B", 0, false},
		{"split pair non-contiguous", "A\x00\x3d\xd8", "\x00\xdeB\x00", "A😀B", 128, false},
		{"complete pair", string(utf16LE("A😀B")), "", "A😀B", 0, false},
		{"pair before lone high", string(utf16LE("A😀")) + "\x3d\xd8", "", "A😀\ufffd", 0, false},
		{"lone high at end", "A\x00\x3d\xd8", "", "A\ufffd", 0, false},
		{"high before BMP", "A\x00\x3d\xd8", "B\x00", "A\ufffdB", 0, false},
		{"high before compressed", "A\x00\x3d\xd8", "B\xe9", "A\ufffdBé", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := wordStreamsWithPieces("", true, 2)
			put32 := binary.LittleEndian.PutUint32
			off := 1024 + len(tc.first) + tc.gap
			copy(s[0][1024:], tc.first)
			copy(s[0][off:], tc.second)
			count, fc := len(tc.second)/2, uint32(off)
			if tc.compressed {
				count, fc = len(tc.second), uint32(off*2)|0x40000000
			}
			put32(s[1][5+4:], uint32(len(tc.first)/2))
			put32(s[1][5+8:], uint32(len(tc.first)/2+count))
			put32(s[1][5+12+8+2:], fc)
			for cap := 1; cap <= 16; cap++ {
				want := strings.ToValidUTF8(tc.want[:min(cap, len(tc.want))], "")
				got, ok := wordPieceText(s[0], nil, s[1], int64(cap))
				if !ok || got != want || len(got) > cap || !utf8.ValidString(got) {
					t.Errorf("cap %d: piece text = (%q, %t), want %q and bounded UTF-8", cap, got, ok, want)
				}
			}
		})
	}
	for _, tc := range []struct {
		name, text   string
		flags, ident uint16
		ok           bool
	}{
		{"overlapping pieces", "subject body text ", 0x0200, 0xa5ec, true},
		{"encrypted FIB", "subject body text ", 0x0300, 0xa5ec, false},
		{"obfuscated FIB", "subject body text ", 0x8200, 0xa5ec, false},
		{"older FIB", "subject body text ", 0x0200, 0xa5dc, false},
		{"control characters only", "\x00\x07\r	\x7f<> ", 0x0200, 0xa5ec, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := wordStreamsWithPieces(tc.text, true, 200)
			binary.LittleEndian.PutUint16(s[0], tc.ident)
			binary.LittleEndian.PutUint16(s[0][0x0a:], tc.flags)
			got, ok := wordPieceText(s[0], nil, s[1], 64)
			if ok != tc.ok || len(got) > 64 || !utf8.ValidString(got) ||
				(ok && !strings.HasPrefix(got, "subject body text subject")) || (!ok && got != "") {
				t.Fatalf("piece text = (%q, %t), want ok=%t and bounded UTF-8", got, ok, tc.ok)
			}
		})
	}
}

func TestHasUTF16LECadence(t *testing.T) {
	mixedLatin := append(bytes.Repeat([]byte{1, 2}, 40), utf16LE("subject body")...)
	for _, tc := range []struct {
		name string
		data []byte
		want bool
	}{
		{"CP1252 text", []byte("caf\xe9 text"), false},
		{"zero-padded CP1252 HTML", []byte("<p>caf\xe9</p>" + strings.Repeat("\x00", 4083)), false},
		{"binary catalog", binaryCatalogMSG, false},
		{"sparse NUL catalog", sparseNULCatalogMSG, false},
		{"several sparse NULs", bytes.Repeat([]byte("arithmetic overflow\x00"), 4), false},
		{"BOM-less UTF-16LE Latin", utf16LE("subject body"), true},
		{"UTF-16LE Latin with binary header", mixedLatin, true},
		{"UTF-16LE CJK HTML", utf16LE("<p>" + strings.Repeat("\u6771\u4eac\u90fd", 30) + "</p>"), true},
		{"UTF-16LE HTML with low-byte NULs", chineseHTMLUTF16LE, true},
		{"UTF-16LE uppercase tag", utf16LE("<P>" + strings.Repeat("\u4e00", 30)), true},
		{"UTF-16LE closing tag", utf16LE(strings.Repeat("\u4e00", 30) + "</p>"), true},
		{"UTF-16LE declaration", utf16LE("<!DOCTYPE html>" + strings.Repeat("\u4e00", 30)), true},
		{"UTF-16LE processing instruction", utf16LE("<?xml?>" + strings.Repeat("\u4e00", 30)), true},
		{"unaligned UTF-16LE markup", append([]byte{1}, chineseHTMLUTF16LE...), false},
		{"UTF-16LE comparison is not markup", utf16LE("<8 " + strings.Repeat("\u4e00", 30)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasUTF16LECadence(tc.data); got != tc.want {
				t.Fatalf("cadence = %t, want %t", got, tc.want)
			}
		})
	}
	if _, ok := tryDecodeUTF16BestEffort(chineseHTMLUTF16LE); ok {
		t.Fatal("CJK fixture must exercise the secondary UTF-16LE reading")
	}
	if got := decodeLegacyStreamText(chineseHTMLUTF16LE); !strings.Contains(got, "\u4e00\u5b63\u5ea6\u62a5\u544a") {
		t.Errorf("legacy decode lost Chinese text: %q", got)
	}
	if got := decodeLegacyStreamText(sparseNULCatalogMSG); !strings.Contains(got, "arithmetic overflow invalid database key") || strings.Contains(got, "\u7261\u7469\u6d68\u7465\u6369") {
		t.Errorf("sparse NUL decode lost 8-bit text or added mojibake: %q", got)
	}
	if got := decodeLegacyStreamText(mixedLatin); !strings.Contains(got, "subject body") {
		t.Errorf("legacy decode lost mixed Latin text: %q", got)
	}
	if _, ok := tryDecodeUTF16BestEffort(mixedLatin); ok {
		t.Fatal("mixed Latin fixture must exercise the secondary UTF-16LE reading")
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
				want := strings.TrimSpace(whitespaceRegex.ReplaceAllString(strings.NewReplacer("<", " ", ">", " ").Replace(tc.want), " "))
				got := decodeLegacyStreamText(tc.data)
				if got != want || strings.ContainsAny(got, "<>") {
					t.Errorf("legacy decode = %q, want %q without angle brackets", got, want)
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
		{"folded ligature exact", []string{"BT /F1 12 Tf (\x93) Tj ET\n"}, 3, "fi"},
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
			_, before := engine.GetPDFStatsDetailed()
			got, err := (&PDFExtractor{}).ExtractReaderAt(bytes.NewReader(data), int64(len(data)), tc.limit)
			_, after := engine.GetPDFStatsDetailed()
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
		{"folded ligature exact", []string{"Q BT /F1 12 Tf (\x93) Tj ET\n"}, 3, "fi"},
		{"exact cumulative", []string{fallback, fallback}, 10, "abcd\nabcd"},
		{"retain preceding text on page error", []string{fallback, broken}, 10, "abcd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := pdfWithPageStreams(tc.streams...)
			engine := &SearchEngine{}
			_, before := engine.GetPDFStatsDetailed()
			got, err := (&PDFExtractor{}).ExtractReaderAt(bytes.NewReader(data), int64(len(data)), tc.limit)
			_, after := engine.GetPDFStatsDetailed()
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

type readCountingReaderAt struct {
	io.ReaderAt
	reads int
}

func (r *readCountingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	return r.ReaderAt.ReadAt(p, off)
}

func pdfWithIndirectWidths(t *testing.T, stream string, compressed bool) []byte {
	t.Helper()
	// Map lowercase glyph codes to uppercase text, not their raw-byte reading.
	const cmap = `begincmap
1 begincodespacerange <00> <ff> endcodespacerange
1 beginbfchar <0a> <000a> endbfchar
1 beginbfrange <61> <7a> <0041> endbfrange
endcmap
`
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /ABCDEF+Times-Roman /FirstChar 32 /LastChar 126 /Widths 6 0 R /ToUnicode 7 0 R >>",
		"[" + strings.Repeat("500 ", 95) + "]",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(cmap), cmap),
	}
	if compressed {
		// Objects 5 (font) and 6 (widths) are reached only via type-2 xrefs.
		header := fmt.Sprintf("5 0 6 %d ", len(objects[4])+1)
		var packed bytes.Buffer
		zw := zlib.NewWriter(&packed)
		if _, err := io.WriteString(zw, header+objects[4]+" "+objects[5]); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, fmt.Sprintf("<< /Type /ObjStm /N 2 /First %d /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(header), packed.Len(), packed.Bytes()))
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		if compressed && (i == 4 || i == 5) {
			continue
		}
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	if compressed {
		offsets = append(offsets, xref) // Object 9 is the xref stream itself.
		entries := make([]byte, 7*len(offsets))
		for id, off := range offsets {
			e := entries[7*id : 7*(id+1)]
			e[0] = 1
			binary.BigEndian.PutUint32(e[1:5], uint32(off))
			switch id {
			case 0:
				e[0] = 0
				binary.BigEndian.PutUint16(e[5:], 65535)
			case 5, 6:
				e[0] = 2
				binary.BigEndian.PutUint32(e[1:5], 8)
				binary.BigEndian.PutUint16(e[5:], uint16(id-5))
			}
		}
		fmt.Fprintf(&b, "9 0 obj\n<< /Type /XRef /Size %d /Root 1 0 R /W [1 4 2] /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(offsets), len(entries), entries)
	} else {
		fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f\n", len(offsets))
		for _, off := range offsets[1:] {
			fmt.Fprintf(&b, "%010d 00000 n\n", off)
		}
		fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\n", len(offsets))
	}
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

func TestPDFPageTextMatchesContentAndResolvesFontsOncePerPage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		compressed bool
	}{
		{"object stream", true},
		{"plain indirect", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads []int
			for _, n := range []int{10, 1000} {
				data := pdfWithIndirectWidths(t, fmt.Sprintf("q 1 0 0 1 10 0 cm BT /F1 10 Tf 2 Tc 110 Tz 1 Ts 72 720 Td (%s) Tj [(x) -333 (y)] TJ 14 TL T* (next) ' 1 0 (quote) \" /F1 10 Tf 0 Tr 2 Tw 1 0 0 1 20 30 Tm 0 -14 TD (end) Tj ET 0 0 1 1 re Q\n", strings.Repeat("a", n)), tc.compressed)
				reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				page := reader.Page(1)
				want := page.Content().Text
				got, err := pdfPageText(page, DefaultMaxExtractedBytes)
				if err != nil || len(got) != len(want) {
					t.Fatalf("%d glyphs: items = %d, want %d; err = %v", n, len(got), len(want), err)
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("%d glyphs, item %d: got %+v, want %+v", n, i, got[i], want[i])
					}
				}
				if _, err := pdfPageText(page, int64(n)); !errors.Is(err, ErrExtractionLimit) {
					t.Fatalf("%d glyphs: budget error = %v", n, err)
				}
				counter := &readCountingReaderAt{ReaderAt: bytes.NewReader(data)}
				text, err := (&PDFExtractor{}).ExtractReaderAt(counter, int64(len(data)), 0)
				wantText := strings.Repeat("A ", n) + "X Y\nN E X T\nQUOTE\nEND"
				if err != nil || text != wantText {
					t.Fatalf("extraction = (%q, %v), want %q", text, err, wantText)
				}
				reads = append(reads, counter.reads)
			}
			if reads[0] != reads[1] {
				t.Fatalf("ReadAt calls scale with glyph count: %v", reads)
			}
			t.Logf("ReadAt calls for 10 and 1000 glyphs: %v", reads)
		})
	}

	for _, stream := range []string{"re", "Tr", "Q", "cm", "Tc", "TD", "Td", "Tf", "\"", "'", "Tj", "TJ", "TL", "Tm", "Ts", "Tw", "Tz"} {
		t.Run("panic/"+stream, func(t *testing.T) {
			data := pdfWithPageStreams(stream + "\n")
			reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			panics := func(f func()) (value any) {
				defer func() { value = recover() }()
				f()
				return nil
			}
			want := panics(func() { reader.Page(1).Content() })
			got := panics(func() { _, _ = pdfPageText(reader.Page(1), DefaultMaxExtractedBytes) })
			if want == nil || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("panic = %v, want %v", got, want)
			}
		})
	}
	for _, stream := range []string{"(" + strings.Repeat(" ", 4097) + ") Tj", strings.Repeat("[] TJ ", 4097)} {
		t.Run("whitespace item budget", func(t *testing.T) {
			data := pdfWithPageStreams("BT /F1 12 Tf " + stream + " ET\n")
			text, err := (&PDFExtractor{}).ExtractReaderAt(bytes.NewReader(data), int64(len(data)), 1)
			if text != "" || !errors.Is(err, ErrExtractionLimit) {
				t.Fatalf("extraction = (%q, %v), want extraction limit", text, err)
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

	var x float64
	for _, run := range []struct {
		text string
		gap  float64
	}{
		{"shrunk", 2.4}, {"space", 2.4}, {"ke", 1.2}, {"rn", 2.4}, {"\ufb01gure", 0},
	} {
		for _, ch := range run.text {
			items = append(items, pdf.Text{X: x, Y: 68, W: 6, FontSize: 12, S: string(ch)})
			x += 6
		}
		x += run.gap
	}
	got := pdfTextItemsToString(items)
	if !strings.Contains(got, "shrunk space kern figure") {
		t.Fatalf("expected shrunk spaces, intact kern and folded ligature, got %q", got)
	}
	if !strings.Contains(got, "Hamiltonian cycles") {
		t.Fatalf("expected word gap without letter spacing, got %q", got)
	}
	if !strings.Contains(got, "\nimpossible") {
		t.Fatalf("expected line break before impossible, got %q", got)
	}
	for i, want := range []string{"ff", "fi", "fl", "ffi", "ffl", "st", "st"} {
		t.Run("ligature/"+string(rune(0xfb00+i)), func(t *testing.T) {
			// Before Tf, the library's no-op encoder preserves raw UTF-8.
			data := pdfWithPageStreams(fmt.Sprintf("BT (%c) Tj ET\n", 0xfb00+i))
			r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(len(want))
			items, err := pdfPageText(r.Page(1), limit)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := pdfTextItemsToStringBounded(items, limit); err != nil || got != want {
				t.Fatalf("folded text = (%q, %v), want %q", got, err, want)
			}
			if _, err := pdfPageText(r.Page(1), limit-1); !errors.Is(err, ErrExtractionLimit) {
				t.Fatalf("short budget error = %v", err)
			}
		})
	}
}

var pdfTestPasswordPad = [32]byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

func pdfPadPassword(pw string) [32]byte {
	var out [32]byte
	n := copy(out[:], pw)
	copy(out[n:], pdfTestPasswordPad[:32-n])
	return out
}

// buildStandardRC4PDF synthesizes a single-page FlateDecode PDF encrypted with
// the PDF 32000-1 Standard Security Handler (RC4, V=1 or V=2, R=2 or R=3).
func buildStandardRC4PDF(t *testing.T, v, r, keyBits int, omitLength bool, userPW, ownerPW, line string) []byte {
	t.Helper()
	keyLen := keyBits / 8
	const fileID = "0123456789abcdef"
	perms := int32(-44)
	if ownerPW == "" {
		ownerPW = userPW
	}
	ownerPad := pdfPadPassword(ownerPW)
	h := md5.Sum(ownerPad[:])
	ownerKey := append([]byte(nil), h[:]...)
	if r >= 3 {
		for i := 0; i < 50; i++ {
			// Algorithm 3 hashes the full digest; truncate only after the rounds.
			sum := md5.Sum(ownerKey)
			ownerKey = append(ownerKey[:0], sum[:]...)
		}
	}
	ownerKey = ownerKey[:keyLen]
	userPad := pdfPadPassword(userPW)
	oEntry := append([]byte(nil), userPad[:]...)
	ciph, err := rc4.NewCipher(ownerKey)
	if err != nil {
		t.Fatal(err)
	}
	ciph.XORKeyStream(oEntry, oEntry)
	if r >= 3 {
		tmpKey := make([]byte, keyLen)
		for i := 1; i <= 19; i++ {
			for j := range ownerKey {
				tmpKey[j] = ownerKey[j] ^ byte(i)
			}
			ciph, _ = rc4.NewCipher(tmpKey)
			ciph.XORKeyStream(oEntry, oEntry)
		}
	}
	var pBytes [4]byte
	binary.LittleEndian.PutUint32(pBytes[:], uint32(perms))
	md := md5.New()
	md.Write(userPad[:])
	md.Write(oEntry)
	md.Write(pBytes[:])
	md.Write([]byte(fileID))
	fileKey := md.Sum(nil)
	if r >= 3 {
		for i := 0; i < 50; i++ {
			sum := md5.Sum(fileKey[:keyLen])
			fileKey = append(fileKey[:0], sum[:]...)
		}
	}
	fileKey = fileKey[:keyLen]
	uEntry := make([]byte, 32)
	if r == 2 {
		copy(uEntry, pdfTestPasswordPad[:])
		ciph, _ = rc4.NewCipher(fileKey)
		ciph.XORKeyStream(uEntry, uEntry)
	} else {
		md.Reset()
		md.Write(pdfTestPasswordPad[:])
		md.Write([]byte(fileID))
		uHash := md.Sum(nil)
		ciph, _ = rc4.NewCipher(fileKey)
		ciph.XORKeyStream(uHash, uHash)
		tmpKey := make([]byte, keyLen)
		for i := 1; i <= 19; i++ {
			for j := range fileKey {
				tmpKey[j] = fileKey[j] ^ byte(i)
			}
			ciph, _ = rc4.NewCipher(tmpKey)
			ciph.XORKeyStream(uHash, uHash)
		}
		copy(uEntry[:16], uHash)
	}
	md.Reset()
	md.Write(fileKey)
	md.Write([]byte{4, 0, 0, 0, 0}) // object 4 0 obj
	objKey := md.Sum(nil)[:min(len(fileKey)+5, 16)]
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	if _, err := fmt.Fprintf(zw, "BT /F1 18 Tf 72 720 Td (%s) Tj ET\n", line); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	encStream := make([]byte, zbuf.Len())
	ciph, err = rc4.NewCipher(objKey)
	if err != nil {
		t.Fatal(err)
	}
	ciph.XORKeyStream(encStream, zbuf.Bytes())
	lengthEntry := fmt.Sprintf(" /Length %d", keyBits)
	if omitLength {
		lengthEntry = ""
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n%s\nendstream", len(encStream), encStream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Filter /Standard /V %d /R %d%s /P %d /O <%x> /U <%x> >>", v, r, lengthEntry, perms, oEntry, uEntry),
	}
	var pdfBuf bytes.Buffer
	pdfBuf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, body := range objs {
		offsets[i+1] = pdfBuf.Len()
		fmt.Fprintf(&pdfBuf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefOff := pdfBuf.Len()
	fmt.Fprintf(&pdfBuf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&pdfBuf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&pdfBuf, "trailer\n<< /Size %d /Root 1 0 R /Encrypt 6 0 R /ID [<%x> <%x>] >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, []byte(fileID), []byte(fileID), xrefOff)
	return pdfBuf.Bytes()
}

func TestStandardRC4PDFOwnerEntry(t *testing.T) {
	// PDF 32000-1:2008, 7.6.3.3, Algorithm 3 (c)-(d): hash the full
	// digest 50 times, then truncate. These fixed /O vectors were generated
	// independently with pypdf 6.1.1, not with this fixture or ledongthuc/pdf:
	// https://github.com/py-pdf/pypdf/blob/6.1.1/pypdf/_encryption.py
	// AlgV4.compute_O_value(AlgV4.compute_O_value_key(b"owner-secret", 3, bits), b"", 3).
	for _, tc := range []struct {
		bits      int
		wantOwner string
	}{
		{40, "2bcab0b937bd9ebeb09b43543f481f3ac2bf43dc4bc0697f3297d2beadfe3a86"},
		{128, "913b073f5b65954c8b6da22169971a177b7e6d94a106ff75c0c16296bc15412e"},
	} {
		t.Run(fmt.Sprintf("%d-bit", tc.bits), func(t *testing.T) {
			data := buildStandardRC4PDF(t, 2, 3, tc.bits, false, "", "owner-secret", "Owner entry control")
			if !bytes.Contains(data, []byte("/O <"+tc.wantOwner+">")) {
				t.Fatalf("fixture /O entry does not match independent Algorithm 3 vector %s", tc.wantOwner)
			}
		})
	}
}

func TestPDFExtractorLegacyFramingTolerance(t *testing.T) {
	const want = "provider access_token pdf hit"
	buildWithHeader := func(hdr string) []byte {
		var b bytes.Buffer
		b.WriteString(hdr)
		stream := fmt.Sprintf("BT /F1 18 Tf 72 720 Td (%s) Tj ET\n", want)
		objs := []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
			fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		}
		offs := make([]int, len(objs))
		for i, o := range objs {
			offs[i] = b.Len()
			fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
		}
		xref := b.Len()
		fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
		for _, off := range offs {
			fmt.Fprintf(&b, "%010d 00000 n \n", off)
		}
		fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
		return b.Bytes()
	}
	incrementalTwoEOF := func() []byte {
		base := buildWithHeader("%PDF-1.4\n")
		rev2ObjOff := len(base)
		stream := fmt.Sprintf("BT /F1 18 Tf 72 720 Td (%s updated) Tj ET\n", want)
		var rev2 bytes.Buffer
		fmt.Fprintf(&rev2, "4 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(stream), stream)
		rev2Xref := len(base) + rev2.Len()
		prevSXIdx := bytes.LastIndex(base, []byte("startxref\n"))
		var prevXref int
		_, _ = fmt.Sscanf(string(base[prevSXIdx:]), "startxref\n%d", &prevXref)
		fmt.Fprintf(&rev2, "xref\n4 1\n%010d 00000 n \ntrailer\n<< /Size 6 /Root 1 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", rev2ObjOff, prevXref, rev2Xref)
		return append(append(base, rev2.Bytes()...), []byte("trailing log noise after second EOF\n")...)
	}
	noFinalEOL := bytes.TrimRight(buildWithHeader("%PDF-1.0 \r\n"), "\r\n")
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"header space CRLF", buildWithHeader("%PDF-1.2 \r\n"), want},
		{"header space LF", buildWithHeader("%PDF-1.0 \n"), want},
		{"header space CR", buildWithHeader("%PDF-1.1 \r"), want},
		{"header spaces and tab", buildWithHeader("%PDF-1.4   \t \r\n"), want},
		{"header max 256 trailing spaces and tab", buildWithHeader("%PDF-1.4" + strings.Repeat(" ", 255) + "\t\r\n"), want},
		{"header space without trailing EOF newline", noFinalEOL, want},
		{"macbinary-like 128B preamble and 13B NUL tail", append(append(bytes.Repeat([]byte{0x00, 'M'}, 64), buildWithHeader("%PDF-1.2\r")...), make([]byte, 13)...), want},
		{"decoy %PDF-1.2 text in preamble before real header", append([]byte("Wrapper for %PDF-1.2 document\n"), buildWithHeader("%PDF-1.4 \n")...), want},
		{"preamble at exact 1016B header window boundary", append(bytes.Repeat([]byte("P"), 1016), buildWithHeader("%PDF-1.4 \n")...), want},
		{"combined 1016B preamble and max 256 header spaces LF", append(bytes.Repeat([]byte("P"), 1016), buildWithHeader("%PDF-1.4"+strings.Repeat(" ", 255)+"\t\n")...), want},
		{"combined 1016B preamble and max 256 header spaces CRLF", append(bytes.Repeat([]byte("P"), 1016), buildWithHeader("%PDF-1.4"+strings.Repeat(" ", 256)+"\r\n")...), want},
		{"415B stderr junk after %%EOF", append(buildWithHeader("%PDF-1.4\r"), bytes.Repeat([]byte("./missing: No such file\n"), 18)[:415]...), want},
		{"immediate non-whitespace after %%EOF", append(bytes.TrimRight(buildWithHeader("%PDF-1.4\n"), "\r\n"), []byte("JUNK")...), want},
		{"trailer junk at exact 1018B after %%EOF LF boundary", append(buildWithHeader("%PDF-1.4\n"), bytes.Repeat([]byte("J"), 1018)...), want},
		{"incremental update keeps final %%EOF", incrementalTwoEOF(), want + " updated"},
	} {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			ext := &PDFExtractor{}
			if got, err := ext.ExtractText(tc.data); err != nil || got != tc.want {
				t.Fatalf("ExtractText = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
			gr := &guardedReaderAt{t: t, r: bytes.NewReader(tc.data), base: 0, end: int64(len(tc.data))}
			if got, err := ext.ExtractReaderAt(gr, int64(len(tc.data)), DefaultMaxExtractedBytes); err != nil || got != tc.want {
				t.Fatalf("ExtractReaderAt = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"short non-PDF", []byte("%PDF-1.")},
		{"absent header", []byte("plain text document without pdf magic\n%%EOF\n")},
		{"unsupported version 1.8", buildWithHeader("%PDF-1.8\n")},
		{"unsupported version 2.0", buildWithHeader("%PDF-2.0\n")},
		{"preamble past window at 1017B", append(bytes.Repeat([]byte("P"), 1017), buildWithHeader("%PDF-1.4\n")...)},
		{"preamble past window at 1017B with 256 spaces", append(bytes.Repeat([]byte("P"), 1017), buildWithHeader("%PDF-1.4"+strings.Repeat(" ", 256)+"\n")...)},
		{"preamble past window at 1024B", append(bytes.Repeat([]byte("P"), 1024), buildWithHeader("%PDF-1.4\n")...)},
		{"header 257 trailing spaces at offset 0", buildWithHeader("%PDF-1.4" + strings.Repeat(" ", 257) + "\n")},
		{"combined 1016B preamble and 257 trailing spaces", append(bytes.Repeat([]byte("P"), 1016), buildWithHeader("%PDF-1.4"+strings.Repeat(" ", 257)+"\r\n")...)},
		{"excessive header spaces without EOL", append([]byte("%PDF-1.4"+strings.Repeat(" ", 300)), buildWithHeader("%PDF-1.4\n")[9:]...)},
		{"missing %%EOF", []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n")},
		{"%%EOF pushed past 1024B tail window", append(buildWithHeader("%PDF-1.4\n"), bytes.Repeat([]byte("J"), 1019)...)},
	} {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			ext := &PDFExtractor{}
			if got, err := ext.ExtractText(tc.data); got != "" || err == nil {
				t.Fatalf("ExtractText = (%q, %v), want empty text and error", got, err)
			}
			gr := &guardedReaderAt{t: t, r: bytes.NewReader(tc.data), base: 0, end: int64(len(tc.data))}
			if got, err := ext.ExtractReaderAt(gr, int64(len(tc.data)), DefaultMaxExtractedBytes); got != "" || err == nil {
				t.Fatalf("ExtractReaderAt = (%q, %v), want empty text and error", got, err)
			}
		})
	}
	// Partial reads must rewrite only the header whitespace, preserving offsets.
	t.Run("framing reader partial overlap", func(t *testing.T) {
		raw := append([]byte("PREAMBLE"), buildWithHeader("%PDF-1.2  	\r\n")...)
		fr, sz, err := normalizePDFReaderAt(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		for off := int64(0); off < 16; off += 3 {
			chunk := make([]byte, 3)
			n, rerr := fr.ReadAt(chunk, off)
			if rerr != nil && rerr != io.EOF {
				t.Fatal(rerr)
			}
			got.Write(chunk[:n])
		}
		if !bytes.HasPrefix(got.Bytes(), []byte("%PDF-1.2\r\r\r\r\n1 0")) {
			t.Fatalf("reconstructed prefix = %q, size = %d", got.Bytes(), sz)
		}
	})
	t.Run("canceled context propagates during framing scan", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, sz := range []int64{4, int64(len(minimalPDFWithText))} {
			got, err := (&PDFExtractor{}).ExtractReaderAt(contextReaderAt{ctx, strings.NewReader(minimalPDFWithText)}, sz, 0)
			if got != "" || !errors.Is(err, context.Canceled) {
				t.Fatalf("size %d: got (%q, %v), want empty and context.Canceled", sz, got, err)
			}
		}
	})
}

func TestPDFExtractorStandardRC4Encryption(t *testing.T) {
	const text = "Leatherwood trail guide"
	for _, tc := range []struct {
		name          string
		v, r, keyBits int
		omitLength    bool
		userPW        string
	}{
		{"empty-user-password/40-bit V1 R2 explicit length", 1, 2, 40, false, ""},
		{"empty-user-password/40-bit V1 R2 default length", 1, 2, 40, true, ""},
		{"empty-user-password/40-bit V2 R3 explicit length", 2, 3, 40, false, ""},
		{"empty-user-password/128-bit V2 R3 control", 2, 3, 128, false, ""},
		{"non-empty-user-password/40-bit V1 R2", 1, 2, 40, false, "user-secret"},
		{"non-empty-user-password/128-bit V2 R3", 2, 3, 128, false, "user-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := buildStandardRC4PDF(t, tc.v, tc.r, tc.keyBits, tc.omitLength, tc.userPW, "owner-secret", text)
			extractor := &PDFExtractor{}
			for _, entry := range []struct {
				name string
				run  func() (string, error)
			}{
				{"ExtractText", func() (string, error) { return extractor.ExtractText(data) }},
				{"ExtractReaderAt", func() (string, error) {
					return extractor.ExtractReaderAt(bytes.NewReader(data), int64(len(data)), DefaultMaxExtractedBytes)
				}},
			} {
				got, err := entry.run()
				if tc.userPW == "" {
					if got != text || err != nil {
						t.Errorf("%s = (%q, %v), want (%q, nil)", entry.name, got, err, text)
					}
				} else if got != "" || !errors.Is(err, pdf.ErrInvalidPassword) {
					t.Errorf("%s = (%q, %v), want empty text and ErrInvalidPassword", entry.name, got, err)
				}
			}
			if tc.userPW != "" {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "locked.pdf"), data, 0o600); err != nil {
					t.Fatal(err)
				}
				engine := NewSearchEngine([]string{"Leatherwood"}, nil, nil, false, 1, 1000)
				engine.StartDir = dir
				engine.Silent = true
				results, err := engine.Execute()
				if err != nil || len(results) != 0 {
					t.Fatalf("locked PDF search = (%v, %v), want no results and nil error", results, err)
				}
			}
		})
	}
}

func encodePDFLZWStream(data []byte, earlyChange int) []byte {
	const (
		clearCode = 256
		eodCode   = 257
		maxWidth  = 12
		tableSize = 1 << maxWidth
	)
	var out bytes.Buffer
	var bitBuf uint32
	var nBits uint
	width := uint(9)
	emit := func(code uint16) {
		bitBuf |= uint32(code) << (32 - width - nBits)
		nBits += width
		for nBits >= 8 {
			out.WriteByte(byte(bitBuf >> 24))
			bitBuf <<= 8
			nBits -= 8
		}
	}
	hi := uint16(eodCode)
	table := make(map[uint32]uint16)
	incHi := func() bool {
		hi++
		if hi == tableSize-1 && earlyChange == 1 {
			emit(clearCode)
			width = 9
			hi = eodCode
			clear(table)
			return true
		}
		if width < maxWidth && hi == (uint16(1)<<width)-uint16(earlyChange) {
			width++
		}
		if hi == tableSize-1 && earlyChange == 0 {
			emit(clearCode)
			width = 9
			hi = eodCode
			clear(table)
			return true
		}
		return false
	}
	emit(clearCode)
	if len(data) > 0 {
		code := uint16(data[0])
		for _, b := range data[1:] {
			key := (uint32(code) << 8) | uint32(b)
			if next, ok := table[key]; ok {
				code = next
				continue
			}
			emit(code)
			code = uint16(b)
			if !incHi() {
				table[key] = hi
			}
		}
		emit(code)
		_ = incHi()
	}
	emit(eodCode)
	if nBits > 0 {
		out.WriteByte(byte(bitBuf >> 24))
	}
	return out.Bytes()
}

func encodeStreamByFilter(t *testing.T, raw []byte, filter string, earlyChange int) []byte {
	t.Helper()
	switch filter {
	case "FlateDecode":
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		if _, err := zw.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	case "LZWDecode":
		return encodePDFLZWStream(raw, earlyChange)
	default:
		t.Fatalf("unsupported test filter %s", filter)
		return nil
	}
}

func buildSingleStreamPDF(objs ...string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestPDFExtractorLZWStreamsAndDecompressionBudgets(t *testing.T) {
	t.Run("syntax floor and saturating cumulative budget", func(t *testing.T) {
		for _, tc := range []struct{ limit, stream, total int64 }{
			{1, 32 * 1024, 128 * 1024},
			{DefaultMaxExtractedBytes, DefaultMaxExtractedBytes, 4 * DefaultMaxExtractedBytes},
			{math.MaxInt64 / 4, math.MaxInt64 / 4, (math.MaxInt64 / 4) * 4},
			{math.MaxInt64/4 + 1, math.MaxInt64/4 + 1, math.MaxInt64},
			{math.MaxInt64, math.MaxInt64, math.MaxInt64},
		} {
			if stream, total := pdfStreamReadBudgets(tc.limit); stream != tc.stream || total != tc.total {
				t.Fatalf("limit %d: budgets (%d, %d), want (%d, %d)", tc.limit, stream, total, tc.stream, tc.total)
			}
		}
	})
	// Vary vocabulary across >600 tokens so LZW crosses 9->10->11-bit width transitions.
	var words []string
	for i := 0; i < 700; i++ {
		words = append(words, fmt.Sprintf("salton%03d", i))
	}
	longSentence := strings.Join(words, " ")
	contentOp := []byte(fmt.Sprintf("BT /F1 11 Tf 36 720 Td (%s) Tj ET\n", longSentence))

	t.Run("content ToUnicode ObjStm XRef chains and predictors", func(t *testing.T) {
		var a85EC1 bytes.Buffer
		e85 := ascii85.NewEncoder(&a85EC1)
		_, _ = e85.Write(encodePDFLZWStream(contentOp, 1))
		_ = e85.Close()
		a85EC1.WriteString("~>")

		var a85EC0 bytes.Buffer
		e850 := ascii85.NewEncoder(&a85EC0)
		_, _ = e850.Write(encodePDFLZWStream(contentOp, 0))
		_ = e850.Close()
		a85EC0.WriteString("~>")

		// PNG Up (Predictor 12, Columns 4) encoded contentOp (padded with spaces to multiple of 4).
		padded := append([]byte(nil), contentOp...)
		for len(padded)%4 != 0 {
			padded = append(padded, ' ')
		}
		var pngUpRaw bytes.Buffer
		var prevRow [4]byte
		for off := 0; off < len(padded); off += 4 {
			pngUpRaw.WriteByte(2)
			for c := 0; c < 4; c++ {
				pngUpRaw.WriteByte(padded[off+c] - prevRow[c])
				prevRow[c] = padded[off+c]
			}
		}
		lzwPNG12 := encodePDFLZWStream(pngUpRaw.Bytes(), 1)
		tiffRaw := append([]byte(nil), padded...)
		for i := len(tiffRaw) - 1; i >= 0; i-- {
			if i%4 != 0 {
				tiffRaw[i] -= padded[i-1]
			}
		}
		lzwTIFF := encodePDFLZWStream(tiffRaw, 1)

		for _, tc := range []struct {
			name string
			obj4 string
		}{
			{"LZW default EarlyChange=1", fmt.Sprintf("<< /Filter /LZWDecode /Length %d >>\nstream\n%s\nendstream", len(encodePDFLZWStream(contentOp, 1)), encodePDFLZWStream(contentOp, 1))},
			{"LZW explicit EarlyChange=1", fmt.Sprintf("<< /Filter /LZWDecode /DecodeParms << /EarlyChange 1 /Predictor 1 >> /Length %d >>\nstream\n%s\nendstream", len(encodePDFLZWStream(contentOp, 1)), encodePDFLZWStream(contentOp, 1))},
			{"LZW explicit EarlyChange=0", fmt.Sprintf("<< /Filter /LZWDecode /DecodeParms << /EarlyChange 0 >> /Length %d >>\nstream\n%s\nendstream", len(encodePDFLZWStream(contentOp, 0)), encodePDFLZWStream(contentOp, 0))},
			{"LZW single-element array with dict DecodeParms", fmt.Sprintf("<< /Filter [/LZWDecode] /DecodeParms << /EarlyChange 0 >> /Length %d >>\nstream\n%s\nendstream", len(encodePDFLZWStream(contentOp, 0)), encodePDFLZWStream(contentOp, 0))},
			{"ASCII85+LZW default EarlyChange=1", fmt.Sprintf("<< /Filter [/ASCII85Decode /LZWDecode] /Length %d >>\nstream\n%s\nendstream", a85EC1.Len(), a85EC1.Bytes())},
			{"ASCII85+LZW empty-dict stage and EarlyChange=0", fmt.Sprintf("<< /Filter [/ASCII85Decode /LZWDecode] /DecodeParms [<< >> << /EarlyChange 0 >>] /Length %d >>\nstream\n%s\nendstream", a85EC0.Len(), a85EC0.Bytes())},
			{"LZW with PNG Up Predictor 12", fmt.Sprintf("<< /Filter /LZWDecode /DecodeParms << /Predictor 12 /Columns 4 >> /Length %d >>\nstream\n%s\nendstream", len(lzwPNG12), lzwPNG12)},
			{"LZW with TIFF Predictor 2", fmt.Sprintf("<< /Filter /LZWDecode /DecodeParms << /Predictor 2 /Columns 4 >> /Length %d >>\nstream\n%s\nendstream", len(lzwTIFF), lzwTIFF)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				pdfData := buildSingleStreamPDF(
					"<< /Type /Catalog /Pages 2 0 R >>",
					"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
					"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
					tc.obj4,
					"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Widths ["+strings.Repeat("500 ", 256)+"] /FirstChar 0 /LastChar 255 >>",
				)
				ext := &PDFExtractor{}
				gotText, err := ext.ExtractText(pdfData)
				if err != nil || gotText != longSentence {
					t.Fatalf("ExtractText = (%q, %v), want (%q, nil)", gotText, err, longSentence)
				}
				gotRA, err := ext.ExtractReaderAt(bytes.NewReader(pdfData), int64(len(pdfData)), DefaultMaxExtractedBytes)
				if err != nil || gotRA != longSentence {
					t.Fatalf("ExtractReaderAt = (%q, %v), want (%q, nil)", gotRA, err, longSentence)
				}
			})
		}

		// LZW ToUnicode CMap + LZW ObjStm (Font + Widths) + LZW XRef stream with Predictor 12.
		for _, ec := range []int{1, 0} {
			t.Run(fmt.Sprintf("LZW ToUnicode ObjStm and XRef EarlyChange=%d", ec), func(t *testing.T) {
				// Pad CMap with harmless comments so LZW crosses the 9->10-bit boundary inside ToUnicode too.
				cmapSrc := fmt.Sprintf("%% %s\nbegincmap\n1 begincodespacerange <00> <ff> endcodespacerange\n1 beginbfchar <0a> <000a> endbfchar\n1 beginbfrange <61> <7a> <0041> endbfrange\nendcmap\n", longSentence)
				cmapLZW := encodePDFLZWStream([]byte(cmapSrc), ec)
				dp := ""
				if ec == 0 {
					dp = " /DecodeParms << /EarlyChange 0 >>"
				}
				fontObj := "<< /Type /Font /Subtype /Type1 /BaseFont /ABCDEF+Times-Roman /FirstChar 32 /LastChar 126 /Widths 6 0 R /ToUnicode 7 0 R >>"
				widthsObj := "[" + strings.Repeat("500 ", 95) + "]"
				objStmHdr := fmt.Sprintf("5 0 6 %d ", len(fontObj)+1)
				objStmLZW := encodePDFLZWStream([]byte(objStmHdr+fontObj+" "+widthsObj+" "+strings.Repeat(" ", 1024)), ec)
				contentLZW := encodePDFLZWStream([]byte("BT /F1 12 Tf 72 720 Td (dataplot) Tj ET\n"), ec)

				var b bytes.Buffer
				b.WriteString("%PDF-1.5\n")
				offsets := make([]int, 10)
				writeRawObj := func(id int, body string) {
					offsets[id] = b.Len()
					fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", id, body)
				}
				writeRawObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
				writeRawObj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
				writeRawObj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
				writeRawObj(4, fmt.Sprintf("<< /Filter /LZWDecode%s /Length %d >>\nstream\n%s\nendstream", dp, len(contentLZW), contentLZW))
				writeRawObj(7, fmt.Sprintf("<< /Filter /LZWDecode%s /Length %d >>\nstream\n%s\nendstream", dp, len(cmapLZW), cmapLZW))
				writeRawObj(8, fmt.Sprintf("<< /Type /ObjStm /N 2 /First %d /Filter /LZWDecode%s /Length %d >>\nstream\n%s\nendstream", len(objStmHdr), dp, len(objStmLZW), objStmLZW))

				xrefOff := b.Len()
				offsets[9] = xrefOff
				var xrRaw bytes.Buffer
				var prevX [7]byte
				for id := 0; id <= 9; id++ {
					var row [7]byte
					row[0] = 1
					binary.BigEndian.PutUint32(row[1:5], uint32(offsets[id]))
					switch id {
					case 0:
						row[0] = 0
						binary.BigEndian.PutUint16(row[5:7], 65535)
					case 5, 6:
						row[0] = 2
						binary.BigEndian.PutUint32(row[1:5], 8)
						binary.BigEndian.PutUint16(row[5:7], uint16(id-5))
					}
					xrRaw.WriteByte(2) // PNG Up
					for c := 0; c < 7; c++ {
						xrRaw.WriteByte(row[c] - prevX[c])
						prevX[c] = row[c]
					}
				}
				xrLZW := encodePDFLZWStream(xrRaw.Bytes(), ec)
				xrDP := "<< /Predictor 12 /Columns 7 >>"
				if ec == 0 {
					xrDP = "<< /EarlyChange 0 /Predictor 12 /Columns 7 >>"
				}
				fmt.Fprintf(&b, "9 0 obj\n<< /Type /XRef /Size 10 /Root 1 0 R /W [1 4 2] /Filter /LZWDecode /DecodeParms %s /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n",
					xrDP, len(xrLZW), xrLZW, xrefOff)

				ext := &PDFExtractor{}
				got, err := ext.ExtractText(b.Bytes())
				if err != nil || got != "DATAPLOT" {
					t.Fatalf("ExtractText = (%q, %v), want (\"DATAPLOT\", nil)", got, err)
				}
				gotMax, err := ext.ExtractReaderAt(bytes.NewReader(b.Bytes()), int64(b.Len()), math.MaxInt64)
				if err != nil || gotMax != "DATAPLOT" {
					t.Fatalf("ExtractReaderAt(MaxInt64) = (%q, %v), want (\"DATAPLOT\", nil)", gotMax, err)
				}
			})
		}
	})

	t.Run("malformed LZW page skips cleanly while preserving sibling pages", func(t *testing.T) {
		goodLZW := encodePDFLZWStream([]byte("BT /F1 12 Tf 72 720 Td (valid page survives) Tj ET\n"), 1)
		truncLZW := goodLZW[:len(goodLZW)/2]
		pdfData := buildSingleStreamPDF(
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 5 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
			fmt.Sprintf("<< /Filter /LZWDecode /Length %d >>\nstream\n%s\nendstream", len(truncLZW), truncLZW),
			fmt.Sprintf("<< /Filter /LZWDecode /Length %d >>\nstream\n%s\nendstream", len(goodLZW), goodLZW),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Widths ["+strings.Repeat("500 ", 256)+"] /FirstChar 0 /LastChar 255 >>",
		)
		got, err := (&PDFExtractor{}).ExtractText(pdfData)
		if err != nil || got != "valid page survives" {
			t.Fatalf("got (%q, %v), want (\"valid page survives\", nil)", got, err)
		}
	})

	t.Run("sparse high-index LZW XRef stream extracts text cleanly", func(t *testing.T) {
		var b bytes.Buffer
		b.WriteString("%PDF-1.5\n")
		off1 := b.Len()
		b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
		off2 := b.Len()
		b.WriteString("2 0 obj\n<< /Type /Pages /Kids [1000000 0 R] /Count 1 >>\nendobj\n")
		offFont := b.Len()
		b.WriteString("3 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Widths [" + strings.Repeat("500 ", 256) + "] /FirstChar 0 /LastChar 255 >>\nendobj\n")
		offPage := b.Len()
		b.WriteString("1000000 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 1000001 0 R /Resources << /Font << /F1 3 0 R >> >> >>\nendobj\n")
		offCont := b.Len()
		contLZW := encodePDFLZWStream([]byte("BT /F1 12 Tf 72 720 Td (sparse xref recovered) Tj ET\n"), 1)
		fmt.Fprintf(&b, "1000001 0 obj\n<< /Filter /LZWDecode /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(contLZW), contLZW)
		xrefOff := b.Len()
		var xrRaw bytes.Buffer
		for _, off := range []int{off1, off2, offFont, offPage, offCont} {
			xrRaw.WriteByte(1)
			var ob [4]byte
			binary.BigEndian.PutUint32(ob[:], uint32(off))
			xrRaw.Write(ob[:])
			xrRaw.Write([]byte{0, 0})
		}
		xrLZW := encodePDFLZWStream(xrRaw.Bytes(), 1)
		fmt.Fprintf(&b, "1000002 0 obj\n<< /Type /XRef /Size 1000003 /Root 1 0 R /W [1 4 2] /Index [1 3 1000000 2] /Filter /LZWDecode /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n",
			len(xrLZW), xrLZW, xrefOff)
		got, err := (&PDFExtractor{}).ExtractReaderAt(bytes.NewReader(b.Bytes()), int64(b.Len()), 64*1024)
		if err != nil || got != "sparse xref recovered" {
			t.Fatalf("got (%q, %v), want (\"sparse xref recovered\", nil)", got, err)
		}
	})

	t.Run("identical decompression bomb limits across LZWDecode and FlateDecode", func(t *testing.T) {
		const budget = 64 * 1024
		buildIntermediateStagePayload := func(filter string, finalOp []byte) []byte {
			if filter == "FlateDecode" {
				inner := []byte{0x78, 0x01}
				for i := 0; i < 10000; i++ {
					inner = append(inner, 0, 0, 0, 0xff, 0xff)
				}
				n := uint16(len(finalOp))
				inner = append(inner, 1, byte(n), byte(n>>8), byte(^n), byte(^n>>8))
				inner = append(inner, finalOp...)
				s1, s2 := uint32(1), uint32(0)
				for _, b := range finalOp {
					s1 = (s1 + uint32(b)) % 65521
					s2 = (s2 + s1) % 65521
				}
				var adler [4]byte
				binary.BigEndian.PutUint32(adler[:], (s2<<16)|s1)
				inner = append(inner, adler[:]...)
				return encodeStreamByFilter(t, inner, "FlateDecode", 1)
			}
			var lzwInner bytes.Buffer
			var bitBuf uint32
			var nBits uint
			emit9 := func(code uint16) {
				bitBuf |= uint32(code) << (32 - 9 - nBits)
				nBits += 9
				for nBits >= 8 {
					lzwInner.WriteByte(byte(bitBuf >> 24))
					bitBuf <<= 8
					nBits -= 8
				}
			}
			for i := 0; i < 40000; i++ {
				emit9(256)
			}
			for _, b := range finalOp {
				emit9(256)
				emit9(uint16(b))
			}
			emit9(257)
			if nBits > 0 {
				lzwInner.WriteByte(byte(bitBuf >> 24))
			}
			return encodePDFLZWStream(lzwInner.Bytes(), 1)
		}

		for _, filter := range []string{"FlateDecode", "LZWDecode"} {
			t.Run(filter, func(t *testing.T) {
				// Exact cap-boundary control: exact 64 KiB stream succeeds, 64 KiB + 1 fails.
				prefix := []byte("BT /F1 12 Tf 72 720 Td (exactcap) Tj ET\n")
				exactRaw := append(append([]byte(nil), prefix...), bytes.Repeat([]byte(" "), budget-len(prefix))...)
				overOneRaw := append(append([]byte(nil), exactRaw...), ' ')
				exactEnc := encodeStreamByFilter(t, exactRaw, filter, 1)
				overOneEnc := encodeStreamByFilter(t, overOneRaw, filter, 1)
				exactPDF := buildSingleStreamPDF(
					"<< /Type /Catalog /Pages 2 0 R >>",
					"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
					"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
					fmt.Sprintf("<< /Filter /%s /Length %d >>\nstream\n%s\nendstream", filter, len(exactEnc), exactEnc),
					"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Widths ["+strings.Repeat("500 ", 256)+"] /FirstChar 0 /LastChar 255 >>",
				)
				if got, err := (&PDFExtractor{}).ExtractReaderAt(bytes.NewReader(exactPDF), int64(len(exactPDF)), budget); err != nil || got != "exactcap" {
					t.Fatalf("exact cap (%d B) got (%q, %v), want (\"exactcap\", nil)", budget, got, err)
				}
				overOnePDF := buildSingleStreamPDF(
					"<< /Type /Catalog /Pages 2 0 R >>",
					"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
					"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
					fmt.Sprintf("<< /Filter /%s /Length %d >>\nstream\n%s\nendstream", filter, len(overOneEnc), overOneEnc),
					"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Widths ["+strings.Repeat("500 ", 256)+"] /FirstChar 0 /LastChar 255 >>",
				)
				if got, err := (&PDFExtractor{}).ExtractReaderAt(bytes.NewReader(overOnePDF), int64(len(overOnePDF)), budget); got != "" || !errors.Is(err, ErrExtractionLimit) {
					t.Errorf("cap+1 (%d B) got (%q, %v), want ErrExtractionLimit", budget+1, got, err)
				}

				// 1. Non-text whitespace operator bomb in positioned content stream (128 KiB).
				spaceBomb := encodeStreamByFilter(t, bytes.Repeat([]byte(" "), budget*2), filter, 1)
				spacePDF := buildSingleStreamPDF(
					"<< /Type /Catalog /Pages 2 0 R >>",
					"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
					"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
					fmt.Sprintf("<< /Filter /%s /Length %d >>\nstream\n%s\nendstream", filter, len(spaceBomb), spaceBomb),
				)
				// 2. Fallback GetPlainText bomb (leading unmatched Q forces fallback).
				fbPayload := append([]byte("Q BT /F1 12 Tf ("), bytes.Repeat([]byte("A"), budget*2)...)
				fbPayload = append(fbPayload, []byte(") Tj ET\n")...)
				fbBomb := encodeStreamByFilter(t, fbPayload, filter, 1)
				fbPDF := buildSingleStreamPDF(
					"<< /Type /Catalog /Pages 2 0 R >>",
					"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
					"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
					fmt.Sprintf("<< /Filter /%s /Length %d >>\nstream\n%s\nendstream", filter, len(fbBomb), fbBomb),
					"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
				)
				// 3. ToUnicode CMap decompression bomb.
				cmapBomb := encodeStreamByFilter(t, append([]byte("begincmap\n"), bytes.Repeat([]byte(" "), budget*2)...), filter, 1)
				shortOp := encodeStreamByFilter(t, []byte("BT /F1 12 Tf (ok) Tj ET\n"), filter, 1)
				tuPDF := buildSingleStreamPDF(
					"<< /Type /Catalog /Pages 2 0 R >>",
					"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
					"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
					fmt.Sprintf("<< /Filter /%s /Length %d >>\nstream\n%s\nendstream", filter, len(shortOp), shortOp),
					"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>",
					fmt.Sprintf("<< /Filter /%s /Length %d >>\nstream\n%s\nendstream", filter, len(cmapBomb), cmapBomb),
				)
				// 4. ObjStm decompression bomb (128 KiB padding before object 5 inside ObjStm).
				objStmHdr := fmt.Sprintf("5 %d ", budget*2)
				objStmBombRaw := append(append([]byte(objStmHdr), bytes.Repeat([]byte(" "), budget*2)...), []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")...)
				objStmBombEnc := encodeStreamByFilter(t, objStmBombRaw, filter, 1)
				var objStmPDF bytes.Buffer
				objStmPDF.WriteString("%PDF-1.5\n")
				o1 := objStmPDF.Len()
				objStmPDF.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
				o2 := objStmPDF.Len()
				objStmPDF.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
				o3 := objStmPDF.Len()
				objStmPDF.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
				o4 := objStmPDF.Len()
				fmt.Fprintf(&objStmPDF, "4 0 obj\n<< /Filter /%s /Length %d >>\nstream\n%s\nendstream\nendobj\n", filter, len(shortOp), shortOp)
				o6 := objStmPDF.Len()
				fmt.Fprintf(&objStmPDF, "6 0 obj\n<< /Type /ObjStm /N 1 /First %d /Filter /%s /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(objStmHdr), filter, len(objStmBombEnc), objStmBombEnc)
				ox := objStmPDF.Len()
				var oxRaw bytes.Buffer
				writeXR := func(tbyte byte, f2 uint32, f3 uint16) {
					oxRaw.WriteByte(tbyte)
					var b4 [4]byte
					var b2 [2]byte
					binary.BigEndian.PutUint32(b4[:], f2)
					binary.BigEndian.PutUint16(b2[:], f3)
					oxRaw.Write(b4[:])
					oxRaw.Write(b2[:])
				}
				writeXR(0, 0, 65535)
				writeXR(1, uint32(o1), 0)
				writeXR(1, uint32(o2), 0)
				writeXR(1, uint32(o3), 0)
				writeXR(1, uint32(o4), 0)
				writeXR(2, 6, 0) // 5 0 R stored in ObjStm 6 (not present before 128 KiB padding -> reads whole stream)
				writeXR(1, uint32(o6), 0)
				writeXR(1, uint32(ox), 0)
				oxEnc := encodeStreamByFilter(t, oxRaw.Bytes(), filter, 1)
				fmt.Fprintf(&objStmPDF, "7 0 obj\n<< /Type /XRef /Size 8 /Root 1 0 R /W [1 4 2] /Filter /%s /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n",
					filter, len(oxEnc), oxEnc, ox)

				// 5. XRef stream decompression bomb (128 KiB decoded XRef bytes).
				xrefBombEntries := (budget*2)/8 + 16
				xrefBombEnc := encodeStreamByFilter(t, make([]byte, xrefBombEntries*8), filter, 1)
				var xrBombPDF bytes.Buffer
				xrBombPDF.WriteString("%PDF-1.5\n")
				xrOff := xrBombPDF.Len()
				fmt.Fprintf(&xrBombPDF, "1 0 obj\n<< /Type /XRef /Size %d /Root 1 0 R /W [1 4 3] /Filter /%s /Length %d >>\nstream\n%s\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n",
					xrefBombEntries, filter, len(xrefBombEnc), xrefBombEnc, xrOff)

				// 6. Multi-page cumulative stream bomb (6 pages * 48 KiB = 288 KiB > 4 * 64 KiB = 256 KiB, each page < 64 KiB).
				page48K := encodeStreamByFilter(t, bytes.Repeat([]byte(" "), 48*1024), filter, 1)
				multiPDF := buildSingleStreamPDF(
					"<< /Type /Catalog /Pages 2 0 R >>",
					"<< /Type /Pages /Kids [3 0 R 3 0 R 3 0 R 3 0 R 3 0 R 3 0 R] /Count 6 >>",
					"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
					fmt.Sprintf("<< /Filter /%s /Length %d >>\nstream\n%s\nendstream", filter, len(page48K), page48K),
				)

				// 7. Chained intermediate-stage cumulative bomb ([/Filter /Filter] x 6 streams:
				// ~45..50 KiB intermediate bytes per stream -> >256 KiB total intermediate work, <300 B final output).
				chainedRaw := buildIntermediateStagePayload(filter, []byte("BT /F1 12 Tf 72 720 Td (budget control) Tj ET\n"))
				chainedStreamObj := fmt.Sprintf("<< /Filter [/%s /%s] /Length %d >>\nstream\n%s\nendstream", filter, filter, len(chainedRaw), chainedRaw)
				chainedPDF := buildSingleStreamPDF(
					"<< /Type /Catalog /Pages 2 0 R >>",
					"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
					"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents [4 0 R 5 0 R 6 0 R 7 0 R 8 0 R 9 0 R] /Resources << /Font << /F1 10 0 R >> >> >>",
					chainedStreamObj,
					chainedStreamObj,
					chainedStreamObj,
					chainedStreamObj,
					chainedStreamObj,
					chainedStreamObj,
					"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
				)

				for _, bc := range []struct {
					name          string
					data          []byte
					wantTruncated bool
				}{
					{"content whitespace bomb", spacePDF, true},
					{"fallback GetPlainText bomb", fbPDF, true},
					{"ToUnicode CMap bomb", tuPDF, true},
					{"ObjStm bomb", objStmPDF.Bytes(), true},
					{"XRef stream bomb", xrBombPDF.Bytes(), false},
					{"cumulative multi-page stream bomb", multiPDF, true},
					{"chained intermediate-stage cumulative bomb", chainedPDF, true},
				} {
					t.Run(bc.name, func(t *testing.T) {
						se := &SearchEngine{}
						_, before := se.GetPDFStatsDetailed()
						r := &pdfEOFProbeReader{ReaderAt: bytes.NewReader(bc.data)}
						got, err := (&PDFExtractor{}).ExtractReaderAt(r, int64(len(bc.data)), budget)
						_, after := se.GetPDFStatsDetailed()
						if got != "" || !errors.Is(err, ErrExtractionLimit) {
							t.Fatalf("got (%q, %v), want empty text and ErrExtractionLimit", got, err)
						}
						if bc.wantTruncated && after <= before {
							t.Fatalf("pdfPagesTruncated delta = %d, want > 0", after-before)
						}
						if bc.name == "fallback GetPlainText bomb" && r.probes != 2 {
							t.Fatalf("page-boundary probes = %d, want positioned and fallback", r.probes)
						}
					})
				}
			})
		}
	})
}
