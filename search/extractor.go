package search

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/emersion/go-mbox"
	"github.com/jhillyerd/enmime"
	"github.com/ledongthuc/pdf"
	"github.com/richardlehane/mscfb"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Extractor defines the interface for extracting text from binary or encoded document formats
type Extractor interface {
	// ExtractText takes raw file bytes and returns extracted plain text
	ExtractText(data []byte) (string, error)
}

const DefaultMaxExtractedBytes int64 = 8 * 1024 * 1024 // 8 MiB default extracted text/stream budget

var ErrExtractionLimit = errors.New("extracted content exceeds maximum byte limit")

// ReaderAtExtractor is an optional capability for extractors that can read
// random-access container formats via io.ReaderAt without buffering the full file in memory.
type ReaderAtExtractor interface {
	Extractor
	ExtractReaderAt(r io.ReaderAt, size int64, maxExtractedBytes int64) (string, error)
}

func effectiveMaxExtractedBytes(limit int64) int64 {
	if limit <= 0 {
		return DefaultMaxExtractedBytes
	}
	return limit
}

func readBoundedAll(r io.Reader, limit int64) ([]byte, error) {
	readLimit := limit
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	buf, err := io.ReadAll(io.LimitReader(r, readLimit))
	if int64(len(buf)) > limit {
		return nil, fmt.Errorf("%w (%d bytes)", ErrExtractionLimit, limit)
	}
	return buf, err
}

// pdfPagesTruncated counts the number of PDF pages truncated for safety.
var pdfPagesTruncated int64

// ExtractorRegistry holds extractors for different file types
type ExtractorRegistry struct {
	extractors map[string]Extractor
}

// NewExtractorRegistry creates a new registry with built-in extractors
func NewExtractorRegistry() *ExtractorRegistry {
	reg := &ExtractorRegistry{
		extractors: make(map[string]Extractor),
	}

	// Register built-in extractors
	reg.registerBuiltIns()

	return reg
}

// HTMLExtractor extracts text from .html files
type HTMLExtractor struct{}

// ExtractText implements the Extractor interface for HTML files
func (e *HTMLExtractor) ExtractText(data []byte) (string, error) {
	html := string(data)
	text := htmlTagRegex.ReplaceAllString(html, " ")
	// Simple entity decoding
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&apos;", "'")
	return strings.TrimSpace(text), nil
}

// XMLExtractor extracts text from .xml files
type XMLExtractor struct{}

// ExtractText implements the Extractor interface for XML files
func (e *XMLExtractor) ExtractText(data []byte) (string, error) {
	xml := string(data)
	text := htmlTagRegex.ReplaceAllString(xml, " ")
	return strings.TrimSpace(text), nil
}

// GetExtractor returns the extractor for a given file extension (without dot)
func (r *ExtractorRegistry) GetExtractor(ext string) (Extractor, bool) {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	extractor, exists := r.extractors[ext]
	return extractor, exists
}

// registerBuiltIns registers the built-in extractors for supported formats
func (r *ExtractorRegistry) registerBuiltIns() {
	// Email formats
	r.extractors["eml"] = &EMLExtractor{}
	r.extractors["mbox"] = &MBOXExtractor{}

	// Binary document formats
	r.extractors["msg"] = &MSGExtractor{}

	// Office document formats
	r.extractors["docx"] = &DOCXExtractor{}
	r.extractors["odt"] = &ODTExtractor{}

	// Web formats
	r.extractors["html"] = &HTMLExtractor{}
	r.extractors["xml"] = &XMLExtractor{}

	// Other
	r.extractors["rtf"] = &RTFExtractor{}
	r.extractors["doc"] = &DOCExtractor{}
	r.extractors["pdf"] = &PDFExtractor{}
}

// IsBinaryFormat checks if a file extension requires text extraction
func IsBinaryFormat(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".msg", ".doc", ".docx", ".odt", ".rtf", ".pdf":
		return true
	case ".eml", ".mbox":
		// EML/MBOX can be text but often encoded
		return true
	default:
		return false
	}
}

// EMLExtractor extracts text from .eml files (MIME messages)
type EMLExtractor struct{}

// ExtractText implements the Extractor interface for EML files
func (e *EMLExtractor) ExtractText(data []byte) (string, error) {
	// Parse the MIME message
	env, err := enmime.ReadEnvelope(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to parse EML: %w", err)
	}

	// Prefer plain text, fallback to HTML if plain text is empty
	text := env.Text
	if text == "" && env.HTML != "" {
		// Strip HTML tags for plain text
		text = stripHTMLTags(env.HTML)
	}

	return mailSearchText(env.GetHeader("Subject"), text), nil
}

var mailSubjectBracketReplacer = strings.NewReplacer("<", " ", ">", " ")

// mailSearchText prepends the decoded subject to the body and collapses whitespace.
// Subject brackets are blanked so HTML and quote cleanup cannot erase mail text.
func mailSearchText(subject, body string) string {
	subject = mailSubjectBracketReplacer.Replace(subject)
	text := strings.TrimSpace(strings.TrimSpace(subject) + "\n\n" + strings.TrimSpace(body))
	return whitespaceRegex.ReplaceAllString(text, " ")
}

// stripHTMLTags removes HTML tags from text (simple implementation)
func stripHTMLTags(html string) string {
	// Remove HTML tags
	text := htmlTagRegex.ReplaceAllString(html, " ")

	// Decode HTML entities
	text = htmlEntityRegex.ReplaceAllStringFunc(text, func(entity string) string {
		switch entity {
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		case "&quot;":
			return "\""
		case "&apos;":
			return "'"
		default:
			return " "
		}
	})

	return text
}

// MBOXExtractor extracts text from .mbox files (collections of MIME messages)
type MBOXExtractor struct{}

// ExtractText implements the Extractor interface for MBOX files
func (e *MBOXExtractor) ExtractText(data []byte) (string, error) {
	reader := mbox.NewReader(bytes.NewReader(data))
	var text strings.Builder

	emlExtractor := &EMLExtractor{}

	for {
		msg, err := reader.NextMessage()
		if err != nil {
			break
		}
		content, err := io.ReadAll(msg)
		if err != nil {
			continue
		}
		emlData := content
		extracted, err := emlExtractor.ExtractText(emlData)
		if err != nil {
			continue
		}
		text.WriteString(extracted)
		text.WriteString("\n---\n")
	}

	if text.Len() == 0 {
		return "", fmt.Errorf("no valid mbox messages found")
	}
	return text.String(), nil
}

// PDFExtractor extracts text from .pdf files
type PDFExtractor struct{}

// DOCExtractor extracts Word 97-2003 piece-table text, with conservative legacy salvage.
type DOCExtractor struct{}

// ExtractText implements the Extractor interface for DOC files.
// Strategy:
// - Open OLE/CFB and read at most 2 MiB in total from the WordDocument, 1Table and 0Table streams
// - Prefer the root Word 97-2003 FIB piece table (Windows-1252 or UTF-16LE pieces)
// - Otherwise decode each stream via decodeLegacyStreamText: UTF-16 when a BOM or dominant NUL lane proves it,
// else the printable-ASCII reading, plus UTF-16LE only with odd-lane NUL cadence
// - If CFB parsing fails or no stream yields text, salvage printable ASCII from a bounded prefix
func (e *DOCExtractor) ExtractText(data []byte) (string, error) {
	return e.ExtractReaderAt(bytes.NewReader(data), int64(len(data)), DefaultMaxExtractedBytes)
}

// ExtractReaderAt extracts likely DOC streams and bounds fallback salvage.
func (e *DOCExtractor) ExtractReaderAt(r io.ReaderAt, size int64, maxExtractedBytes int64) (string, error) {
	if r == nil || size < 0 {
		return "", fmt.Errorf("invalid reader or size: %d", size)
	}
	maxTotal := min(effectiveMaxExtractedBytes(maxExtractedBytes), 2*1024*1024)
	cf, err := mscfb.New(r)
	if err != nil {
		// Fall back to crude ASCII salvage if we cannot parse CFB
		data, readErr := io.ReadAll(io.NewSectionReader(r, 0, min(size, maxTotal)))
		if readErr != nil {
			return "", readErr
		}
		return salvagePrintableASCII(data, false), nil
	}

	var parts [][]byte
	root := make(map[string][]byte)
	total := int64(0)
	targetStreams := map[string]bool{
		"WordDocument": true,
		"1Table":       true,
		"0Table":       true,
	}

	for ent, err2 := cf.Next(); err2 == nil; ent, err2 = cf.Next() {
		if total >= maxTotal {
			break
		}
		name := ent.Name
		if !targetStreams[name] {
			continue
		}
		remain := maxTotal - total
		if remain <= 0 {
			break
		}
		b, _ := io.ReadAll(io.LimitReader(ent, remain))
		total += int64(len(b))
		if len(b) == 0 {
			continue
		}

		parts = append(parts, b)
		if len(ent.Path) == 0 && root[name] == nil {
			root[name] = b
		}
	}
	if text, ok := wordPieceText(root["WordDocument"], root["0Table"], root["1Table"], maxTotal); ok {
		return text, nil
	}

	var out strings.Builder
	for _, b := range parts {
		text := decodeLegacyStreamText(b, true)
		if text != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(text)
		}
	}

	result := strings.TrimSpace(out.String())
	if result == "" {
		// Last-resort fallback: plain ASCII salvage on a bounded prefix.
		data, readErr := io.ReadAll(io.NewSectionReader(r, 0, min(size, maxTotal)))
		if readErr != nil {
			return "", readErr
		}
		result = salvagePrintableASCII(data, true)
	}
	return result, nil
}

// wordPieceText decodes a Word 97-2003 FIB/CLX piece table. Unsupported or
// malformed tables leave legacy salvage authoritative; bounded prefixes stop
// at the first unavailable piece or at the output byte budget.
func wordPieceText(word, table0, table1 []byte, maxBytes int64) (string, bool) {
	le16, le32 := binary.LittleEndian.Uint16, binary.LittleEndian.Uint32
	if len(word) < 34 || le16(word) != 0xa5ec || maxBytes <= 0 {
		return "", false
	}
	flags := le16(word[0x0a:])
	if flags&0x8100 != 0 { // fEncrypted | fObfuscated
		return "", false
	}
	table := table0
	if flags&0x0200 != 0 { // fWhichTblStm
		table = table1
	}
	lw := 34 + 2*int(le16(word[32:]))
	if len(word) < lw+2 {
		return "", false
	}
	fcLcb := lw + 2 + 4*int(le16(word[lw:]))
	if len(word) < fcLcb+2+34*8 || le16(word[fcLcb:]) < 34 {
		return "", false
	}
	// FibRgFcLcb97 entry 33 locates CLX in the selected table stream.
	fcClx := int64(le32(word[fcLcb+2+33*8:]))
	lcbClx := int64(le32(word[fcLcb+2+33*8+4:]))
	if fcClx > int64(len(table)) || lcbClx > int64(len(table))-fcClx {
		return "", false
	}
	clx := table[fcClx : fcClx+lcbClx]
	for len(clx) > 0 && clx[0] == 0x01 { // Skip Prc property records.
		if len(clx) < 3 {
			return "", false
		}
		cb := int(int16(le16(clx[1:])))
		if cb < 0 || cb > len(clx)-3 {
			return "", false
		}
		clx = clx[3+cb:]
	}
	if len(clx) < 5 || clx[0] != 0x02 {
		return "", false
	}
	lcb := int64(le32(clx[1:]))
	if lcb < 16 || lcb > int64(len(clx)-5) || (lcb-4)%12 != 0 {
		return "", false
	}
	plc := clx[5 : 5+lcb]
	n := int((lcb - 4) / 12)
	var out strings.Builder
	for i := 0; i < n; i++ {
		start, end := le32(plc[4*i:]), le32(plc[4*(i+1):])
		if end < start {
			return "", false
		}
		fc := le32(plc[4*(n+1)+8*i+2:])
		off, count := int64(fc), int64(end-start)
		compressed := fc&0x40000000 != 0
		if compressed {
			off = int64(fc&0x3fffffff) / 2
		} else {
			count *= 2
		}
		if off > int64(len(word)) || count > int64(len(word))-off {
			break
		}
		data := word[off : off+count]
		var piece string
		if compressed {
			piece, _ = charmap.Windows1252.NewDecoder().String(string(data))
		} else {
			units := make([]uint16, len(data)/2)
			for j := range units {
				units[j] = le16(data[2*j:])
			}
			piece = string(utf16.Decode(units))
		}
		remain := maxBytes - int64(out.Len())
		if int64(len(piece)) > remain {
			out.WriteString(strings.ToValidUTF8(piece[:remain], ""))
			break
		}
		out.WriteString(piece)
		if int64(out.Len()) == maxBytes {
			break
		}
	}
	text := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '<' || r == '>' {
			return ' '
		}
		return r
	}, out.String())
	text = strings.TrimSpace(whitespaceRegex.ReplaceAllString(text, " "))
	return text, text != ""
}

// ExtractText implements the Extractor interface for PDF files
func (e *PDFExtractor) ExtractText(data []byte) (string, error) {
	return e.ExtractReaderAt(bytes.NewReader(data), int64(len(data)), DefaultMaxExtractedBytes)
}

// ExtractReaderAt extracts PDF text page-by-page with a cumulative budget.
func (e *PDFExtractor) ExtractReaderAt(r io.ReaderAt, size int64, maxExtractedBytes int64) (out string, err error) {
	// Guard against any panics from the PDF library.
	defer func() {
		if r := recover(); r != nil {
			out = ""
			err = fmt.Errorf("pdf extraction panic: %v", r)
		}
	}()

	if r == nil || size < 0 {
		return "", fmt.Errorf("invalid reader or size: %d", size)
	}
	limit := effectiveMaxExtractedBytes(maxExtractedBytes)
	reader, rerr := pdf.NewReader(r, size)
	if rerr != nil {
		return "", rerr
	}

	var b strings.Builder

	// Safely obtain number of pages (library may panic on malformed PDFs).
	pages := 0
	func() {
		defer func() { _ = recover() }()
		pages = reader.NumPage()
	}()

	if pages <= 0 {
		return "", fmt.Errorf("invalid or empty PDF: %d pages", pages)
	}

	// Extract positioned text page-by-page. GetPlainText can collapse adjacent
	// PDF text runs together, which breaks whole-word matching.
	for i := 1; i <= pages; i++ {
		// Probe even when the PDF library can serve this page from its cache.
		if _, err := r.ReadAt(nil, 0); err != nil && err != io.EOF {
			return "", err
		}
		var pageErr error
		func() {
			defer func() { _ = recover() }()
			page := reader.Page(i)
			if page.V.IsNull() {
				return
			}
			remaining := limit - int64(b.Len())
			items, perr := pdfPageText(page, remaining)
			if perr != nil {
				pageErr = perr
				return
			}
			pageText, perr := pdfTextItemsToStringBounded(items, remaining)
			if perr != nil {
				pageErr = perr
				return
			}
			if pageText != "" {
				if int64(b.Len()+len(pageText)+1) > limit {
					pageErr = ErrExtractionLimit
					return
				}
				b.WriteString(pageText)
				b.WriteByte('\n')
			}
		}()
		if pageErr != nil {
			atomic.AddInt64(&pdfPagesTruncated, int64(pages-i+1))
			return "", fmt.Errorf("%w (%d bytes)", ErrExtractionLimit, limit)
		}
	}

	extracted := strings.TrimSpace(b.String())
	if extracted != "" {
		return extracted, nil
	}

	// Fallback to the library's plain text path if positioned text is empty.
	fonts := make(map[string]*pdf.Font)
	var plainBuf strings.Builder
	for i := 1; i <= pages; i++ {
		// Probe even when the PDF library can serve this page from its cache.
		if _, err := r.ReadAt(nil, 0); err != nil && err != io.EOF {
			return "", err
		}
		var pagePlain string
		var pageErr error
		func() {
			defer func() { _ = recover() }()
			p := reader.Page(i)
			if p.V.IsNull() {
				return
			}
			for _, name := range p.Fonts() {
				if _, ok := fonts[name]; !ok {
					f := p.Font(name)
					fonts[name] = &f
				}
			}
			pagePlain, pageErr = p.GetPlainText(fonts)
			pagePlain = pdfLigatureFolder.Replace(pagePlain)
		}()
		if pageErr != nil {
			break
		}
		if int64(plainBuf.Len()+len(pagePlain)) > limit {
			atomic.AddInt64(&pdfPagesTruncated, int64(pages-i+1))
			return "", fmt.Errorf("%w (%d bytes)", ErrExtractionLimit, limit)
		}
		plainBuf.WriteString(pagePlain)
	}
	if s := strings.TrimSpace(plainBuf.String()); s != "" {
		return s, nil
	}
	return "", nil
}

/*
The PDF matrix and page interpreter below are adapted from

	github.com/ledongthuc/pdf, page.go (5959a4027728).

Copyright 2014 The Go Authors. All rights reserved.

Upstream BSD license:
Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

  - Redistributions of source code must retain the above copyright

notice, this list of conditions and the following disclaimer.
  - Redistributions in binary form must reproduce the above

copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
  - Neither the name of Google Inc. nor the names of its

contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
*/
type pdfMatrix [3][3]float64

var pdfIdentity = pdfMatrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (x pdfMatrix) mul(y pdfMatrix) pdfMatrix {
	var z pdfMatrix
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				z[i][j] += x[i][k] * y[k][j]
			}
		}
	}
	return z
}

type pdfFontMetrics struct {
	name        string
	enc         pdf.TextEncoding
	first, last int
	widths      pdf.Value
}

func (f *pdfFontMetrics) width(code int) float64 {
	if f == nil || code < f.first || code > f.last {
		return 0
	}
	return f.widths.Index(code - f.first).Float64()
}

type pdfTextState struct {
	Tc, Th, Tl, Tfs, Trise float64
	Tf                     *pdfFontMetrics
	Tm, Tlm, CTM           pdfMatrix
}

var errPDFPageBudget = errors.New("pdf page text budget")

// pdfPageText mirrors ledongthuc/pdf's Page.Content text operators and panic
// behavior, but resolves font metrics once per page instead of once per glyph.
// Keep byte-indexed widths and TJ's synthetic newline for extraction parity.
func pdfPageText(p pdf.Page, maxBytes int64) (text []pdf.Text, err error) {
	defer func() {
		if r := recover(); r != nil {
			if r != errPDFPageBudget {
				panic(r)
			}
			text, err = nil, fmt.Errorf("%w (%d bytes)", ErrExtractionLimit, maxBytes)
		}
	}()
	if p.V.IsNull() || p.V.Key("Contents").Kind() == pdf.Null {
		return nil, nil
	}
	strm := p.V.Key("Contents")
	fonts := make(map[string]*pdfFontMetrics)
	var enc pdf.TextEncoding
	g := pdfTextState{Th: 1, CTM: pdfIdentity}
	var rawBytes int64
	// Whitespace and empty TJ arrays also emit items. Bound those allocations
	// independently, while allowing ordinary whitespace after an exact budget.
	maxItems := max(maxBytes, 4096)
	showText := func(s string) {
		decoded := s
		if enc != nil {
			decoded = enc.Decode(s)
		}
		n := 0
		for _, ch := range decoded {
			rawBytes += int64(len(pdfLigatureFolder.Replace(strings.TrimSpace(string(ch)))))
			if rawBytes > maxBytes || int64(len(text)) >= maxItems {
				panic(errPDFPageBudget)
			}
			var w0 float64
			if n < len(s) {
				w0 = g.Tf.width(int(s[n]))
			}
			n++
			var name string
			if g.Tf != nil {
				name = g.Tf.name
			}
			trm := pdfMatrix{{g.Tfs * g.Th, 0, 0}, {0, g.Tfs, 0}, {0, g.Trise, 1}}.mul(g.Tm).mul(g.CTM)
			text = append(text, pdf.Text{Font: name, FontSize: trm[0][0], X: trm[2][0], Y: trm[2][1], W: w0 / 1000 * trm[0][0], S: string(ch)})
			tx := (w0/1000*g.Tfs + g.Tc) * g.Th
			g.Tm = pdfMatrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(g.Tm)
		}
	}
	var stack []pdfTextState
	pdf.Interpret(strm, func(stk *pdf.Stack, op string) {
		args := make([]pdf.Value, stk.Len())
		for i := len(args) - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		switch op {
		case "cm":
			if len(args) != 6 {
				panic("bad g.Tm")
			}
			var m pdfMatrix
			for i := 0; i < 6; i++ {
				m[i/2][i%2] = args[i].Float64()
			}
			m[2][2] = 1
			g.CTM = m.mul(g.CTM)
		case "re":
			if len(args) != 4 {
				panic("bad re")
			}
		case "q":
			stack = append(stack, g)
		case "Q":
			n := len(stack) - 1
			g = stack[n]
			stack = stack[:n]
		case "BT":
			g.Tm = pdfIdentity
			g.Tlm = g.Tm
		case "T*":
			g.Tlm = pdfMatrix{{1, 0, 0}, {0, 1, 0}, {0, -g.Tl, 1}}.mul(g.Tlm)
			g.Tm = g.Tlm
		case "Tc":
			if len(args) != 1 {
				panic("bad g.Tc")
			}
			g.Tc = args[0].Float64()
		case "TD":
			if len(args) != 2 {
				panic("bad Td")
			}
			g.Tl = -args[1].Float64()
			fallthrough
		case "Td":
			if len(args) != 2 {
				panic("bad Td")
			}
			tx, ty := args[0].Float64(), args[1].Float64()
			g.Tlm = pdfMatrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}.mul(g.Tlm)
			g.Tm = g.Tlm
		case "Tf":
			if len(args) != 2 {
				panic("bad TL")
			}
			name := args[0].Name()
			f, ok := fonts[name]
			if !ok {
				font := p.Font(name)
				base := font.BaseFont()
				if i := strings.Index(base, "+"); i >= 0 {
					base = base[i+1:]
				}
				f = &pdfFontMetrics{name: base, enc: font.Encoder(), first: font.FirstChar(), last: font.LastChar(), widths: font.V.Key("Widths")}
				fonts[name] = f
			}
			g.Tf, enc = f, f.enc
			g.Tfs = args[1].Float64()
		case "\"":
			if len(args) != 3 {
				panic("bad \" operator")
			}
			g.Tc = args[1].Float64()
			args = args[2:]
			fallthrough
		case "'":
			if len(args) != 1 {
				panic("bad ' operator")
			}
			g.Tlm = pdfMatrix{{1, 0, 0}, {0, 1, 0}, {0, -g.Tl, 1}}.mul(g.Tlm)
			g.Tm = g.Tlm
			fallthrough
		case "Tj":
			if len(args) != 1 {
				panic("bad Tj operator")
			}
			showText(args[0].RawString())
		case "TJ":
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == pdf.String {
					showText(x.RawString())
				} else {
					tx := -x.Float64() / 1000 * g.Tfs * g.Th
					g.Tm = pdfMatrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(g.Tm)
				}
			}
			showText("\n")
		case "TL":
			if len(args) != 1 {
				panic("bad TL")
			}
			g.Tl = args[0].Float64()
		case "Tm":
			if len(args) != 6 {
				panic("bad g.Tm")
			}
			var m pdfMatrix
			for i := 0; i < 6; i++ {
				m[i/2][i%2] = args[i].Float64()
			}
			m[2][2] = 1
			g.Tm, g.Tlm = m, m
		case "Tr":
			if len(args) != 1 {
				panic("bad Tr")
			}
		case "Ts":
			if len(args) != 1 {
				panic("bad Ts")
			}
			g.Trise = args[0].Float64()
		case "Tw":
			if len(args) != 1 {
				panic("bad g.Tw")
			}
		case "Tz":
			if len(args) != 1 {
				panic("bad Tz")
			}
			g.Th = args[0].Float64() / 100
		}
	})
	return text, nil
}

// TeX justification can shrink a 0.25 em Times space below 0.25 em.
const pdfWordGapEm = 0.15

var pdfLigatureFolder = strings.NewReplacer(
	"\ufb00", "ff", "\ufb01", "fi", "\ufb02", "fl", "\ufb03", "ffi",
	"\ufb04", "ffl", "\ufb05", "st", "\ufb06", "st",
)

func pdfTextItemsToString(items []pdf.Text) string {
	text, _ := pdfTextItemsToStringBounded(items, DefaultMaxExtractedBytes)
	return text
}

func pdfTextItemsToStringBounded(items []pdf.Text, maxBytes int64) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	// Reject oversized pages before allocating and sorting a copy of the items.
	var rawBytes int64
	for _, item := range items {
		rawBytes += int64(len(pdfLigatureFolder.Replace(strings.TrimSpace(item.S))))
		if rawBytes > maxBytes {
			return "", fmt.Errorf("%w (%d bytes)", ErrExtractionLimit, maxBytes)
		}
	}
	if rawBytes == 0 {
		return "", nil
	}
	text := append([]pdf.Text(nil), items...)
	sort.Sort(pdf.TextVertical(text))

	var b strings.Builder
	var prev pdf.Text
	havePrev := false
	pendingSpace := false
	for _, item := range text {
		s := pdfLigatureFolder.Replace(strings.TrimSpace(item.S))
		if s == "" {
			if item.S != "" {
				pendingSpace = true
			}
			continue
		}
		var separator byte
		if havePrev {
			lineThreshold := item.FontSize * 0.5
			if lineThreshold < 2 {
				lineThreshold = 2
			}
			if absFloat(item.Y-prev.Y) > lineThreshold {
				separator = '\n'
			} else {
				gap := item.X - (prev.X + prev.W)
				if pendingSpace || gap > item.FontSize*pdfWordGapEm {
					separator = ' '
				}
			}
		}
		nextBytes := int64(b.Len()) + int64(len(s))
		if separator != 0 {
			nextBytes++
		}
		if nextBytes > maxBytes {
			return "", fmt.Errorf("%w (%d bytes)", ErrExtractionLimit, maxBytes)
		}
		if separator != 0 {
			b.WriteByte(separator)
		}
		b.WriteString(s)
		pendingSpace = false
		prev = item
		havePrev = true
	}
	return strings.TrimSpace(b.String()), nil
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// MSGExtractor extracts Subject and body text from .msg files (Outlook OLE/CFB messages).
// MAPI property types select decoding: Unicode 001F as UTF-16LE, ANSI 001E as UTF-8 or
// Windows-1252. Binary 0102 (HTML only) is detected UTF-16, else ANSI,
// plus a UTF-16LE reading only with odd-lane NUL cadence.
// HTML is used only without a plain body. Non-CFB input, or a message with no Subject/body
// text, falls back to bounded legacy-stream decoding.
type MSGExtractor struct{}

// ExtractText implements the Extractor interface for MSG files
func (e *MSGExtractor) ExtractText(data []byte) (string, error) {
	return e.ExtractReaderAt(bytes.NewReader(data), int64(len(data)), DefaultMaxExtractedBytes)
}

// ExtractReaderAt reads only target MAPI streams and bounds fallback salvage.
func (e *MSGExtractor) ExtractReaderAt(r io.ReaderAt, size int64, maxExtractedBytes int64) (string, error) {
	if r == nil || size < 0 {
		return "", fmt.Errorf("invalid reader or size: %d", size)
	}
	limit := effectiveMaxExtractedBytes(maxExtractedBytes)
	// Parse the OLE compound file and decode Subject/Body (then HTML) by MAPI property type.
	if cf, err := mscfb.New(r); err == nil {
		streams := make(map[string][]byte)
		var totalRead int64
		for ent, err2 := cf.Next(); err2 == nil; ent, err2 = cf.Next() {
			if len(ent.Path) != 0 {
				continue
			}
			name := ent.Name
			switch name {
			case "__substg1.0_0037001F", "__substg1.0_0037001E", "__substg1.0_1000001F", "__substg1.0_1000001E", "__substg1.0_1013001F", "__substg1.0_1013001E", "__substg1.0_10130102":
			default:
				continue
			}
			b, err := readBoundedAll(ent, limit-totalRead)
			if err != nil {
				return "", err
			}
			totalRead += int64(len(b))
			if len(b) > 0 {
				streams[name] = b
			}
		}

		// Helper: prefer Unicode (001F), then ANSI (001E), then binary 0102 (for HTML)
		findStream := func(keys ...string) (string, []byte, bool) {
			for _, k := range keys {
				if v, ok := streams[k]; ok && len(v) > 0 {
					return k, v, true
				}
			}
			return "", nil, false
		}
		// MAPI string types determine encoding; only binary properties need detection.
		decodeMSGText := func(name string, b []byte) string {
			switch {
			case strings.HasSuffix(name, "001F"):
				r := transform.NewReader(bytes.NewReader(b), unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder())
				s, _ := io.ReadAll(r)
				return strings.TrimSpace(string(s))
			case strings.HasSuffix(name, "001E"):
				// ANSI properties must never be interpreted as UTF-16.
			default:
				if s, ok := tryDecodeUTF16BestEffort(b); ok {
					return strings.TrimSpace(s)
				}
				// Retain both readings only when odd-lane NULs show UTF-16LE cadence:
				// non-Latin UTF-16LE can be valid UTF-8, but its markup has NUL high bytes.
				// 8-bit HTML gets the single ANSI reading.
				// Strip each reading's tags before combining, while markup is intact.
				// Leave entities encoded for the final HTML pass to decode once.
				if hasUTF16LECadence(b) {
					text := stripReadingMarkup(decodeANSIText(b)) + "\n" +
						stripReadingMarkup(utf16LEReadingWithMarkup(b, true))
					return strings.TrimSpace(whitespaceRegex.ReplaceAllString(text, " "))
				}
			}
			s := whitespaceRegex.ReplaceAllString(decodeANSIText(b), " ")
			return strings.TrimSpace(s)
		}

		var subject, body string

		// PR_SUBJECT: 0037 (Unicode 001F; ANSI 001E)
		if name, b, ok := findStream("__substg1.0_0037001F", "__substg1.0_0037001E"); ok {
			subject = decodeMSGText(name, b)
		}
		// PR_BODY: 1000 (Unicode 001F; ANSI 001E)
		if name, b, ok := findStream("__substg1.0_1000001F", "__substg1.0_1000001E"); ok {
			body = decodeMSGText(name, b)
		}
		// PR_HTML: 1013 (Unicode 001F; ANSI 001E; sometimes 0102 binary); use as fallback for body
		if body == "" {
			if name, b, ok := findStream("__substg1.0_1013001F", "__substg1.0_1013001E", "__substg1.0_10130102"); ok {
				html := decodeMSGText(name, b)
				if html == "" {
					html = string(b)
				}
				body = strings.TrimSpace(stripHTMLTags(html))
			}
		}

		if subject != "" || body != "" {
			return mailSearchText(subject, body), nil
		}
	}

	// Fallback: bounded shared legacy-stream decoding.
	fallbackMax := min(limit, 2*1024*1024)
	data, readErr := io.ReadAll(io.NewSectionReader(r, 0, min(size, fallbackMax)))
	if readErr != nil {
		return "", readErr
	}
	return decodeLegacyStreamText(data, false), nil
}

// decodeANSIText keeps valid UTF-8 (including ASCII) and decodes other 8-bit
// MAPI ANSI text as Windows-1252, the default Outlook code page.
func decodeANSIText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if s, err := charmap.Windows1252.NewDecoder().Bytes(b); err == nil {
		return string(s)
	}
	return string(b)
}

// stripReadingMarkup removes style/script contents, tags and stray brackets before readings are joined.
// Entities stay encoded for the downstream HTML pass to decode once.
func stripReadingMarkup(s string) string {
	s = cssRegex.ReplaceAllString(s, " ")
	s = jsRegex.ReplaceAllString(s, " ")
	s = htmlTagRegex.ReplaceAllString(s, " ")
	return strings.NewReplacer("<", " ", ">", " ").Replace(s)
}

// salvagePrintableASCII collapses non-printable bytes to spaces. Binary streams
// also blank stray angle brackets so the Stage 3 HTML cleaner cannot erase text.
func salvagePrintableASCII(b []byte, binary bool) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c >= 0x21 && c <= 0x7e && !(binary && (c == '<' || c == '>')) {
			out = append(out, c)
		} else if len(out) > 0 && out[len(out)-1] != ' ' {
			out = append(out, ' ')
		}
	}
	return strings.TrimSpace(string(out))
}

// utf16LEReading retains the legacy UTF-16LE view without binary controls or
// angle brackets that could cause the HTML cleaner to erase searchable text.
func utf16LEReading(b []byte) string {
	return utf16LEReadingWithMarkup(b, false)
}

// utf16LEReadingWithMarkup optionally preserves angle brackets for HTML stripping.
func utf16LEReadingWithMarkup(b []byte, keepMarkup bool) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	var out strings.Builder
	pendingSpace := false
	for _, r := range utf16.Decode(units) {
		if r <= 0x20 || r == 0x7f || r == utf8.RuneError || (!keepMarkup && (r == '<' || r == '>')) {
			pendingSpace = out.Len() > 0
			continue
		}
		if pendingSpace {
			out.WriteByte(' ')
			pendingSpace = false
		}
		out.WriteRune(r)
	}
	return out.String()
}

// decodeLegacyStreamText is the shared Stage 1/2/3 legacy-stream rule: UTF-16 when
// a BOM or dense NUL lane proves it; otherwise one 8-bit reading, plus a UTF-16LE
// reading only when odd-lane NULs dominate. Word 97 files use the piece table first.
func decodeLegacyStreamText(b []byte, binary bool) string {
	if s, ok := tryDecodeUTF16BestEffort(b); ok {
		if binary {
			s = strings.NewReplacer("<", " ", ">", " ").Replace(s)
		}
		return strings.TrimSpace(whitespaceRegex.ReplaceAllString(s, " "))
	}
	if !binary {
		text := stripReadingMarkup(legacyEightBitText(b))
		if hasUTF16LECadence(b) {
			text += "\n" + stripReadingMarkup(utf16LEReadingWithMarkup(b, true))
		}
		return strings.TrimSpace(text)
	}
	text := salvagePrintableASCII(b, binary)
	if hasUTF16LECadence(b) {
		text += "\n" + utf16LEReading(b)
	}
	return strings.TrimSpace(text)
}

// legacyEightBitText decodes NUL-free streams as UTF-8, else Windows-1252,
// and salvages printable ASCII from streams containing NULs (binary data).
func legacyEightBitText(b []byte) string {
	if bytes.IndexByte(b, 0) >= 0 {
		return salvagePrintableASCII(b, false)
	}
	s := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, decodeANSIText(b))
	return strings.TrimSpace(whitespaceRegex.ReplaceAllString(s, " "))
}

// hasUTF16LECadence reports NULs concentrated in the odd (high-byte) lane, the
// signature of UTF-16LE ASCII-range code units, ignoring trailing NUL padding.
func hasUTF16LECadence(b []byte) bool {
	even, odd := utf16ZeroLanes(bytes.TrimRight(b, "\x00"))
	return odd > 4*even
}

// tryDecodeUTF16BestEffort requires a BOM or a dominant NUL lane before decoding.
// UseBOM alone never rejects missing BOMs, so it cannot detect UTF-16.
func tryDecodeUTF16BestEffort(b []byte) (string, bool) {
	// UseBOM honors either byte order, but only actual BOMs justify this path.
	if len(b) >= 2 && ((b[0] == 0xff && b[1] == 0xfe) || (b[0] == 0xfe && b[1] == 0xff)) {
		r := transform.NewReader(bytes.NewReader(b), unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder())
		s, err := io.ReadAll(r)
		if err == nil {
			str := strings.TrimSpace(string(s))
			if str != "" {
				return str, true
			}
		}
	}

	// Heuristic: many nulls on odd bytes => likely UTF-16LE without BOM
	if isLikelyUTF16LE(b) {
		r := transform.NewReader(bytes.NewReader(b), unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder())
		s, err := io.ReadAll(r)
		if err == nil {
			str := strings.TrimSpace(string(s))
			if str != "" {
				return str, true
			}
		}
	}

	// Heuristic: many nulls on even bytes => likely UTF-16BE without BOM
	if isLikelyUTF16BE(b) {
		r := transform.NewReader(bytes.NewReader(b), unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder())
		s, err := io.ReadAll(r)
		if err == nil {
			str := strings.TrimSpace(string(s))
			if str != "" {
				return str, true
			}
		}
	}

	return "", false
}

func utf16ZeroLanes(b []byte) (even, odd int) {
	for i, c := range b {
		if c == 0 {
			if i%2 == 0 {
				even++
			} else {
				odd++
			}
		}
	}
	return even, odd
}

func isLikelyUTF16LE(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	even, odd := utf16ZeroLanes(b)
	return float64(odd) >= 0.30*float64(len(b)/2) && odd > 4*even
}

func isLikelyUTF16BE(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	even, odd := utf16ZeroLanes(b)
	return float64(even) >= 0.30*float64(len(b)/2) && even > 4*odd
}

// DOCXExtractor extracts text from .docx files (Office Open XML)
type DOCXExtractor struct{}

// ExtractText implements the Extractor interface for DOCX files
func (e *DOCXExtractor) ExtractText(data []byte) (string, error) {
	return e.ExtractReaderAt(bytes.NewReader(data), int64(len(data)), DefaultMaxExtractedBytes)
}

// ExtractReaderAt reads the bounded document XML without buffering media.
func (e *DOCXExtractor) ExtractReaderAt(r io.ReaderAt, size int64, maxExtractedBytes int64) (string, error) {
	if r == nil || size < 0 {
		return "", fmt.Errorf("invalid reader or size: %d", size)
	}
	limit := effectiveMaxExtractedBytes(maxExtractedBytes)
	zipReader, err := zip.NewReader(r, size)
	if err != nil {
		return "", err
	}

	for _, file := range zipReader.File {
		if file.Name == "word/document.xml" {
			rc, err := file.Open()
			if err != nil {
				return "", err
			}
			content, err := readBoundedAll(rc, limit)
			rc.Close()
			if err != nil {
				return "", err
			}
			text := htmlTagRegex.ReplaceAllString(string(content), " ")
			return strings.TrimSpace(text), nil
		}
	}

	return "", fmt.Errorf("docx archive missing word/document.xml")
}

// ODTExtractor extracts text from .odt files (OpenDocument Text)
type ODTExtractor struct{}

// ExtractText implements the Extractor interface for ODT files
func (e *ODTExtractor) ExtractText(data []byte) (string, error) {
	return e.ExtractReaderAt(bytes.NewReader(data), int64(len(data)), DefaultMaxExtractedBytes)
}

// ExtractReaderAt reads the bounded content XML without buffering media.
func (e *ODTExtractor) ExtractReaderAt(r io.ReaderAt, size int64, maxExtractedBytes int64) (string, error) {
	if r == nil || size < 0 {
		return "", fmt.Errorf("invalid reader or size: %d", size)
	}
	limit := effectiveMaxExtractedBytes(maxExtractedBytes)
	zipReader, err := zip.NewReader(r, size)
	if err != nil {
		return "", err
	}

	for _, file := range zipReader.File {
		if file.Name == "content.xml" {
			rc, err := file.Open()
			if err != nil {
				return "", err
			}
			content, err := readBoundedAll(rc, limit)
			rc.Close()
			if err != nil {
				return "", err
			}
			text := htmlTagRegex.ReplaceAllString(string(content), " ")
			return strings.TrimSpace(text), nil
		}
	}

	return "", fmt.Errorf("odt archive missing content.xml")
}

// RTFExtractor extracts text from .rtf files (Rich Text Format)
type RTFExtractor struct{}

var rtfControlRegex = regexp.MustCompile(`\\[a-z]+\d*`)

// ExtractText implements the Extractor interface for RTF files
func (e *RTFExtractor) ExtractText(data []byte) (string, error) {
	text := string(data)

	// Remove RTF control words (simple regex approach)
	text = rtfControlRegex.ReplaceAllString(text, "")

	// Remove braces
	text = strings.ReplaceAll(text, "{", "")
	text = strings.ReplaceAll(text, "}", "")

	// Clean up excessive whitespace
	text = whitespaceRegex.ReplaceAllString(text, " ")

	return strings.TrimSpace(text), nil
}
