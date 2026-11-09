package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

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
