////go:build pdfcpu
//go:build pdfcpu
// +build pdfcpu

package pdf

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Default caps for PDF text extraction.
const (
	DefaultPageCap    = 200        // maximum number of pages to process
	DefaultPerPageCap = 128 * 1024 // 128 KiB per-page text cap
	DefaultTimeout    = 150 * time.Millisecond
)

// pdfWorkerPath is the path to the pdfworker binary.
// This will be set during initialization based on the current executable's location.
var pdfWorkerPath string

// workerPathInitOnce ensures we only determine the worker path once.
var workerPathInitOnce sync.Once

// getPdfWorkerPath determines the path to the pdfworker binary.
func getPdfWorkerPath() string {
	workerPathInitOnce.Do(func() {
		// Try to find pdfworker relative to current executable
		execPath, err := os.Executable()
		if err == nil {
			dir := filepath.Dir(execPath)
			// First try same directory
			candidate := filepath.Join(dir, "pdfworker")
			if _, err := os.Stat(candidate); err == nil {
				pdfWorkerPath = candidate
				return
			}
			// Then try ../pdfworker (development layout)
			candidate = filepath.Join(dir, "..", "pdfworker")
			if _, err := os.Stat(candidate); err == nil {
				pdfWorkerPath = candidate
				return
			}
		}
		// Fallback: look in PATH
		path, err := exec.LookPath("pdfworker")
		if err == nil {
			pdfWorkerPath = path
			return
		}
		// Last resort: assume it's in the same directory as the garp binary
		if execPath != "" {
			pdfWorkerPath = filepath.Join(filepath.Dir(execPath), "pdfworker")
		}
	})
	return pdfWorkerPath
}

// ExtractAllTextCapped extracts text from a PDF using a subprocess for memory isolation.
// This runs the PDF processing in a separate short-lived process that is killed after
// completion or timeout, ensuring all memory is released between PDFs.
func ExtractAllTextCapped(path string, pageCap, perPageCap int, words []string, window int) (string, bool, error) {
	// Defaults
	if pageCap <= 0 {
		pageCap = DefaultPageCap
	}
	if perPageCap <= 0 {
		perPageCap = DefaultPerPageCap
	}

	// Pre-flight checks
	info, err := os.Stat(path)
	if err != nil {
		return "", false, nil
	}
	// Skip files > 50MB
	if info.Size() > 50*1024*1024 {
		return "", false, nil
	}

	workerPath := getPdfWorkerPath()
	if workerPath == "" {
		// Worker not found, fall back to nil error (undecided)
		return "", false, nil
	}

	// Build arguments: <path> <pageCap> <perPageCap> <distance> <words...>
	args := []string{
		path,
		fmt.Sprintf("%d", pageCap),
		fmt.Sprintf("%d", perPageCap),
		fmt.Sprintf("%d", window),
	}
	args = append(args, words...)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	// Run in subprocess for memory isolation
	cmd := exec.CommandContext(ctx, workerPath, args...)
	cmd.Stderr = os.Stderr // Pass through any errors

	output, err := cmd.Output()
	if err != nil {
		// Check for timeout
		if ctx.Err() == context.DeadlineExceeded {
			// Timeout - treat as undecided
			return "", false, nil
		}
		// Other error - treat as undecided
		return "", false, nil
	}

	// Parse output: FORMAT|ERROR|text
	// FORMAT is one of: MATCHED, NOMATCH, ERROR
	outputStr := strings.TrimSpace(string(output))
	if outputStr == "" {
		return "", false, nil
	}

	parts := strings.SplitN(outputStr, "|", 3)
	if len(parts) < 2 {
		return "", false, nil
	}

	format := parts[0]
	// errMsg := parts[1] // Could log this for debugging

	switch format {
	case "MATCHED":
		// All words found within distance
		// Return full text for context/excerpt generation (parts[2] contains text)
		if len(parts) >= 3 {
			return parts[2], true, nil
		}
		return "", true, nil
	case "NOMATCH":
		// Words not found within distance (or partial text)
		// For prefilter, we just need to know it's NOT a match
		return "", false, nil
	case "ERROR":
		// Extraction error - treat as undecided
		return "", false, nil
	default:
		// Unknown format, treat as undecided
		return "", false, nil
	}
}

// ExtractAllTextCappedWithContext extracts text from a PDF using a subprocess with explicit context.
// This variant allows the caller to control the timeout.
func ExtractAllTextCappedWithContext(ctx context.Context, path string, pageCap, perPageCap int, words []string, window int) (string, bool, error) {
	// Defaults
	if pageCap <= 0 {
		pageCap = DefaultPageCap
	}
	if perPageCap <= 0 {
		perPageCap = DefaultPerPageCap
	}

	// Pre-flight checks
	info, err := os.Stat(path)
	if err != nil {
		return "", false, nil
	}
	if info.Size() > 50*1024*1024 {
		return "", false, nil
	}

	workerPath := getPdfWorkerPath()
	if workerPath == "" {
		return "", false, nil
	}

	// Build arguments
	args := []string{
		path,
		fmt.Sprintf("%d", pageCap),
		fmt.Sprintf("%d", perPageCap),
		fmt.Sprintf("%d", window),
	}
	args = append(args, words...)

	cmd := exec.CommandContext(ctx, workerPath, args...)
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", false, nil
		}
		return "", false, nil
	}

	outputStr := strings.TrimSpace(string(output))
	if outputStr == "" {
		return "", false, nil
	}

	parts := strings.SplitN(outputStr, "|", 3)
	if len(parts) < 2 {
		return "", false, nil
	}

	format := parts[0]
	switch format {
	case "MATCHED":
		// Return full text for context/excerpt generation
		if len(parts) >= 3 {
			return parts[2], true, nil
		}
		return "", true, nil
	case "NOMATCH":
		return "", false, nil
	case "ERROR":
		return "", false, nil
	default:
		return "", false, nil
	}
}
