package search

import (
	"archive/zip"
	"bytes"
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

	// Clean up excessive whitespace
	text = strings.TrimSpace(text)
	text = whitespaceRegex.ReplaceAllString(text, " ")

	return text, nil
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

// DOCExtractor extracts text from legacy .doc (OLE/CFB) files using conservative salvage
type DOCExtractor struct{}

// ExtractText implements the Extractor interface for DOC files.
// Strategy:
// - Open OLE/CFB and read a bounded amount of likely streams (WordDocument, 1Table, 0Table)
// - Try UTF-16 decode; else ASCII salvage replacing non-printables with spaces
// - Collapse whitespace and return a concise plain-text representation
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

	var out strings.Builder

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
			content := page.Content()
			remaining := limit - int64(b.Len())
			pageText, perr := pdfTextItemsToStringBounded(content.Text, remaining)
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
		rawBytes += int64(len(strings.TrimSpace(item.S)))
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
		s := strings.TrimSpace(item.S)
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
				if pendingSpace || gap > item.FontSize*0.25 {
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

// MSGExtractor extracts text from .msg files (Outlook messages)
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
	// Attempt to parse the OLE compound file and extract Unicode Subject/Body first.
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
				// Retain both readings for invalid UTF-8 or NUL-containing streams:
				// non-Latin UTF-16LE can be valid UTF-8, but its HTML tags have NULs.
				// Strip each reading's tags before combining, while markup is intact.
				// Leave entities encoded for the final HTML pass to decode once.
				if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
					text := htmlTagRegex.ReplaceAllString(string(b), " ") + "\n" +
						htmlTagRegex.ReplaceAllString(utf16LEReadingWithMarkup(b, true), " ")
					return strings.TrimSpace(whitespaceRegex.ReplaceAllString(text, " "))
				}
			}
			s := whitespaceRegex.ReplaceAllString(string(b), " ")
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
			out := strings.TrimSpace(strings.TrimSpace(subject) + "\n\n" + strings.TrimSpace(body))
			out = whitespaceRegex.ReplaceAllString(out, " ")
			return out, nil
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

// decodeLegacyStreamText is the shared Stage 1/2/3 legacy-stream rule. Without
// encoding evidence, retain both readings; Word's piece table is the exact
// alternative for distinguishing mixed 8-bit and UTF-16LE text from binary noise.
func decodeLegacyStreamText(b []byte, binary bool) string {
	if s, ok := tryDecodeUTF16BestEffort(b); ok {
		if binary {
			s = strings.NewReplacer("<", " ", ">", " ").Replace(s)
		}
		return strings.TrimSpace(whitespaceRegex.ReplaceAllString(s, " "))
	}
	return strings.TrimSpace(salvagePrintableASCII(b, binary) + "\n" + utf16LEReading(b))
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
