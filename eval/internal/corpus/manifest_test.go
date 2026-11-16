package corpus

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestEligible(t *testing.T) {
	for _, tc := range []struct {
		corpus, path string
		valid        bool
	}{
		{"docs", "govdocs/x.pdf", true}, {"docs", "x.toml", false},
		{"code", "ripgrep/Dockerfile", true}, {"code", "ripgrep/target/x.rs", false},
		{"code", "ripgrep/src/x.rs", true}, {"code", "garp/README.md", true},
		{"code", "ripgrep/Dockerfile.dev", true}, {"code", "ripgrep/Containerfile", true},
		{"docs", "govdocs/X.PDF", true}, {"docs", "govdocs/.cache/x.pdf", false},
		{"code", "vendor/x.go", false}, {"code", "garp/bin/garp", false},
		{"code", "garp/garp.png", false}, {"docs", "Dockerfile", false},
		{"other", "x.pdf", false}, {"docs", "../x.pdf", false},
		{"docs", "/x.pdf", false}, {"code", `garp\x.go`, false},
	} {
		t.Run(tc.corpus+"/"+tc.path, func(t *testing.T) {
			if err := Eligible(tc.corpus, tc.path); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifest.json")
	raw := []byte(`{"corpus":"docs","sources":[{"id":"source","kind":"zip-range","url":"https://example.invalid/x.zip","base_url":"https://example.invalid/","size":123,"etag":"etag","sha256":"hash","ref":"v1","commit":"commit"}],"files":[{"path":"govdocs/x.pdf","source":"source","member":"x.pdf","size":10,"sha256":"filehash"}]}`)
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	m, hash, err := Load(p)
	if err != nil || hash != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		t.Fatalf("%s %v", hash, err)
	}
	wantSource := Source{ID: "source", Kind: "zip-range", URL: "https://example.invalid/x.zip", BaseURL: "https://example.invalid/", Size: 123, ETag: "etag", SHA256: "hash", Ref: "v1", Commit: "commit"}
	wantFile := File{Path: "govdocs/x.pdf", Source: "source", Member: "x.pdf", Size: 10, SHA256: "filehash"}
	if m.Corpus != "docs" || len(m.Sources) != 1 || m.Sources[0] != wantSource || len(m.Files) != 1 || m.Files[0] != wantFile {
		t.Fatalf("%+v", m)
	}
	for _, invalid := range []string{`{"corpus":`, `{} {}`} {
		if err := os.WriteFile(p, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(p); err == nil {
			t.Fatal("accepted invalid JSON")
		}
	}
	if _, _, err := Load(p + "missing"); err == nil {
		t.Fatal("accepted missing file")
	}
}
