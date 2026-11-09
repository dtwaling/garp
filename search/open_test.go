package search

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRegularSearchFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "candidate.txt")
	const content = "searchable content"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Run("regular", func(t *testing.T) {
		file, info, err := openRegularSearchFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if !info.Mode().IsRegular() || info.Size() != int64(len(content)) {
			t.Fatalf("unexpected descriptor metadata: %v", info)
		}
		got, err := io.ReadAll(file)
		if err != nil || string(got) != content {
			t.Fatalf("read = %q, %v", got, err)
		}
	})
	for name, target := range map[string]string{"directory": dir, "missing": filepath.Join(dir, "missing")} {
		t.Run(name, func(t *testing.T) {
			assertSearchOpenRejected(t, target)
		})
	}
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(dir, "link.txt")
		if err := os.Symlink(path, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertSearchOpenRejected(t, link)
		if content, _, err := GetFileContentWithLimit(link, 1024); err == nil || content != "" {
			t.Fatalf("symlink content = %q, error = %v", content, err)
		}
	})
}

func assertSearchOpenRejected(t *testing.T, path string) {
	t.Helper()
	file, info, err := openRegularSearchFile(path)
	if file != nil {
		file.Close()
	}
	if err == nil || file != nil || info != nil {
		t.Fatalf("open(%q) = %v, %v, %v; want nil, nil, error", path, file, info, err)
	}
}
