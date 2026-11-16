// Package corpus describes the pinned files used by the retrieval harness.
package corpus

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"garp/config"
)

type Manifest struct {
	Corpus  string   `json:"corpus"`
	Sources []Source `json:"sources"`
	Files   []File   `json:"files"`
}

// Source holds the pins required by its Kind; unused fields are omitted.
type Source struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	URL     string `json:"url,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Size    int64  `json:"size,omitempty"`
	ETag    string `json:"etag,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

type File struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Member string `json:"member"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Load parses a manifest and hashes its exact bytes, including whitespace.
// Source/file integrity and eligibility are checked by the caller, not Load.
func Load(filename string) (Manifest, string, error) {
	var m Manifest
	b, err := os.ReadFile(filename)
	if err != nil {
		return m, "", err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, "", fmt.Errorf("manifest %s: %w", filename, err)
	}
	return m, fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// Eligible checks a canonical, slash-separated corpus-relative file path
// against garp's discovery rules. It performs no filesystem or content reads.
func Eligible(corpus, filename string) error {
	if corpus != "docs" && corpus != "code" {
		return fmt.Errorf("unknown corpus %q", corpus)
	}
	if !fs.ValidPath(filename) || filename == "." || strings.Contains(filename, `\`) {
		return fmt.Errorf("invalid corpus path %q", filename)
	}
	parts := strings.Split(filename, "/")
	for _, dir := range parts[:len(parts)-1] {
		if config.ShouldSkipDirectory(dir) {
			return fmt.Errorf("%s: skipped directory %q", filename, dir)
		}
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(filename)), ".")
	if slices.Contains(config.DocumentTypes, ext) || (corpus == "code" && config.IsCodeFile(filename)) {
		return nil
	}
	return fmt.Errorf("%s: ineligible for %s corpus", filename, corpus)
}
