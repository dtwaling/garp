// Package pdf preserves the legacy optional PDF worker integration.
//
// The default search path uses search.PDFExtractor and github.com/ledongthuc/pdf
// directly. This package is intentionally not wired into the engine today; it
// remains available for review or salvage if subprocess-isolated PDF extraction
// is revisited.
package pdf
