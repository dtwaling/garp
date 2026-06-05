package search

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
	text := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(html, " ")
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
	text := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(xml, " ")
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
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")

	return text, nil
}

// stripHTMLTags removes HTML tags from text (simple implementation)
func stripHTMLTags(html string) string {
	// Remove HTML tags
	tagRegex := regexp.MustCompile(`<[^>]*>`)
	text := tagRegex.ReplaceAllString(html, " ")

	// Decode HTML entities
	entityRegex := regexp.MustCompile(`&[a-zA-Z0-9#]*;`)
	text = entityRegex.ReplaceAllStringFunc(text, func(entity string) string {
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
		return string(data), nil
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
	// Open CFB from a byte reader
	cf, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		// Fall back to crude ASCII salvage if we cannot parse CFB
		buf := make([]rune, 0, len(data))
		for _, b := range data {
			if b == 0x09 || b == 0x0a || b == 0x0d || (b >= 0x20 && b <= 0x7e) {
				buf = append(buf, rune(b))
			} else {
				buf = append(buf, ' ')
			}
		}
		txt := strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(string(buf), " "))
		return txt, nil
	}

	var out strings.Builder
	const maxTotal = 2 * 1024 * 1024 // 2 MiB budget across streams
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

		var text string
		if s, ok := tryDecodeUTF16BestEffort(b); ok {
			text = s
		} else {
			// ASCII salvage
			buf := make([]rune, 0, len(b))
			for _, bb := range b {
				if bb == 0x09 || bb == 0x0a || bb == 0x0d || (bb >= 0x20 && bb <= 0x7e) {
					buf = append(buf, rune(bb))
				} else {
					buf = append(buf, ' ')
				}
			}
			text = string(buf)
		}

		// Normalize and append
		text = strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(text, " "))
		if text != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(text)
		}
	}

	result := strings.TrimSpace(out.String())
	if result == "" {
		// last-resort fallback: plain ASCII salvage on the whole file
		buf := make([]rune, 0, len(data))
		for _, b := range data {
			if b == 0x09 || b == 0x0a || b == 0x0d || (b >= 0x20 && b <= 0x7e) {
				buf = append(buf, rune(b))
			} else {
				buf = append(buf, ' ')
			}
		}
		result = strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(string(buf), " "))
	}
	return result, nil
}

// ExtractText implements the Extractor interface for PDF files
func (e *PDFExtractor) ExtractText(data []byte) (out string, err error) {
	// Default to raw content so we always return something readable on failure.
	out = string(data)

	// Guard against any panics from the PDF library.
	defer func() {
		if r := recover(); r != nil {
			// Keep the default 'out' (raw content) and no error.
			err = nil
		}
	}()

	reader, rerr := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if rerr != nil {
		// Fall back to raw content on reader construction error.
		return out, nil
	}

	var b strings.Builder

	// Safely obtain number of pages (library may panic on malformed PDFs).
	pages := 0
	func() {
		defer func() { _ = recover() }()
		pages = reader.NumPage()
	}()

	if pages <= 0 {
		return out, nil
	}

	// Extract positioned text page-by-page. GetPlainText can collapse adjacent
	// PDF text runs together, which breaks whole-word matching.
	for i := 1; i <= pages; i++ {
		func() {
			defer func() { _ = recover() }()
			page := reader.Page(i)
			if page.V.IsNull() {
				return
			}
			content := page.Content()
			pageText := pdfTextItemsToString(content.Text)
			if pageText != "" {
				b.WriteString(pageText)
				b.WriteString("\n")
			}
		}()
	}

	extracted := strings.TrimSpace(b.String())
	if extracted != "" {
		return extracted, nil
	}

	// Fallback to the library's plain text path if positioned text is empty.
	if plain, gerr := reader.GetPlainText(); gerr == nil {
		pb, _ := io.ReadAll(plain)
		s := strings.TrimSpace(string(pb))
		if s != "" {
			return s, nil
		}
	}
	return out, nil
}

func pdfTextItemsToString(items []pdf.Text) string {
	if len(items) == 0 {
		return ""
	}
	text := append([]pdf.Text(nil), items...)
	sort.Sort(pdf.TextVertical(text))

	var b strings.Builder
	var prev pdf.Text
	havePrev := false
	for _, item := range text {
		s := strings.TrimSpace(item.S)
		if s == "" {
			continue
		}
		if havePrev {
			lineThreshold := item.FontSize * 0.5
			if lineThreshold < 2 {
				lineThreshold = 2
			}
			if absFloat(item.Y-prev.Y) > lineThreshold {
				b.WriteByte('\n')
			} else {
				gap := item.X - (prev.X + prev.W)
				if gap > item.FontSize*0.25 {
					b.WriteByte(' ')
				}
			}
		}
		b.WriteString(s)
		prev = item
		havePrev = true
	}
	return strings.TrimSpace(b.String())
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
	// Attempt to parse the OLE compound file and extract Unicode Subject/Body first.
	if cf, err := mscfb.New(bytes.NewReader(data)); err == nil {
		streams := make(map[string][]byte)
		for ent, err2 := cf.Next(); err2 == nil; ent, err2 = cf.Next() {
			name := ent.Name
			// Read the entire stream content
			b, _ := io.ReadAll(ent)
			if len(b) > 0 {
				streams[name] = b
			}
		}

		// Helper: prefer Unicode (001F), then ANSI (001E), then binary 0102 (for HTML)
		findStream := func(keys ...string) ([]byte, bool) {
			for _, k := range keys {
				if v, ok := streams[k]; ok && len(v) > 0 {
					return v, true
				}
			}
			return nil, false
		}
		// Helper: decode text from MSG stream, UTF-16 aware with ASCII fallback
		decodeMSGText := func(b []byte) string {
			if s, ok := tryDecodeUTF16BestEffort(b); ok {
				return strings.TrimSpace(s)
			}
			s := regexp.MustCompile(`\s+`).ReplaceAllString(string(b), " ")
			return strings.TrimSpace(s)
		}

		var subject, body string

		// PR_SUBJECT: 0037 (Unicode 001F; ANSI 001E)
		if b, ok := findStream("__substg1.0_0037001F", "__substg1.0_0037001E"); ok {
			subject = decodeMSGText(b)
		}
		// PR_BODY: 1000 (Unicode 001F; ANSI 001E)
		if b, ok := findStream("__substg1.0_1000001F", "__substg1.0_1000001E"); ok {
			body = decodeMSGText(b)
		}
		// PR_HTML: 1013 (Unicode 001F; ANSI 001E; sometimes 0102 binary); use as fallback for body
		if body == "" {
			if b, ok := findStream("__substg1.0_1013001F", "__substg1.0_1013001E", "__substg1.0_10130102"); ok {
				html := decodeMSGText(b)
				if html == "" {
					html = string(b)
				}
				body = strings.TrimSpace(stripHTMLTags(html))
			}
		}

		if subject != "" || body != "" {
			out := strings.TrimSpace(strings.TrimSpace(subject) + "\n\n" + strings.TrimSpace(body))
			out = regexp.MustCompile(`\s+`).ReplaceAllString(out, " ")
			return out, nil
		}
	}

	// Fallback: best-effort UTF-16, then ASCII salvage (spaces for non-printables)
	if s, ok := tryDecodeUTF16BestEffort(data); ok {
		return strings.TrimSpace(s), nil
	}
	buf := make([]rune, 0, len(data))
	for _, b := range data {
		if b == 0x09 || b == 0x0a || b == 0x0d || (b >= 0x20 && b <= 0x7e) {
			buf = append(buf, rune(b))
		} else {
			buf = append(buf, ' ')
		}
	}
	out := regexp.MustCompile(`\s+`).ReplaceAllString(string(buf), " ")
	return strings.TrimSpace(out), nil
}

// tryDecodeUTF16BestEffort attempts BOM-aware UTF-16 decoding, then heuristic LE/BE.
func tryDecodeUTF16BestEffort(b []byte) (string, bool) {
	// Prefer BOM-aware decode (handles LE/BE automatically if BOM present)
	{
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

func isLikelyUTF16LE(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	zeros := 0
	slots := 0
	for i := 1; i < len(b); i += 2 {
		slots++
		if b[i] == 0x00 {
			zeros++
		}
	}
	return slots > 0 && float64(zeros) >= 0.30*float64(slots)
}

func isLikelyUTF16BE(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	zeros := 0
	slots := 0
	for i := 0; i < len(b); i += 2 {
		slots++
		if b[i] == 0x00 {
			zeros++
		}
	}
	return slots > 0 && float64(zeros) >= 0.30*float64(slots)
}

// DOCXExtractor extracts text from .docx files (Office Open XML)
type DOCXExtractor struct{}

// ExtractText implements the Extractor interface for DOCX files
func (e *DOCXExtractor) ExtractText(data []byte) (string, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return string(data), nil
	}

	for _, file := range zipReader.File {
		if file.Name == "word/document.xml" {
			rc, err := file.Open()
			if err != nil {
				continue
			}
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				continue
			}
			text := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(string(content), " ")
			return strings.TrimSpace(text), nil
		}
	}

	return string(data), nil
}

// ODTExtractor extracts text from .odt files (OpenDocument Text)
type ODTExtractor struct{}

// ExtractText implements the Extractor interface for ODT files
func (e *ODTExtractor) ExtractText(data []byte) (string, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return string(data), nil
	}

	for _, file := range zipReader.File {
		if file.Name == "content.xml" {
			rc, err := file.Open()
			if err != nil {
				continue
			}
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				continue
			}
			text := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(string(content), " ")
			return strings.TrimSpace(text), nil
		}
	}

	return string(data), nil
}

// RTFExtractor extracts text from .rtf files (Rich Text Format)
type RTFExtractor struct{}

// ExtractText implements the Extractor interface for RTF files
func (e *RTFExtractor) ExtractText(data []byte) (string, error) {
	text := string(data)

	// Remove RTF control words (simple regex approach)
	rtfControlRegex := regexp.MustCompile(`\\[a-z]+\d*`)
	text = rtfControlRegex.ReplaceAllString(text, "")

	// Remove braces
	text = strings.ReplaceAll(text, "{", "")
	text = strings.ReplaceAll(text, "}", "")

	// Clean up excessive whitespace
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")

	return strings.TrimSpace(text), nil
}
