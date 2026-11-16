package corpus

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func TestFetch(t *testing.T) {
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	w, _ := zw.Create("x.txt")
	w.Write([]byte("hello\n"))
	zw.Close()
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "top/x.txt", Mode: 0600, Size: 6})
	tw.Write([]byte("hello\n"))
	tw.Close()
	gw.Close()
	bz, err := os.ReadFile("testdata/sample.tar.bz2")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, corpus string }{{"raw", "docs"}, {"zip-range", "docs"}, {"tar-gz", "code"}, {"tar-bz2", "docs"}} {
		t.Run(tc.kind, func(t *testing.T) {
			kind := tc.kind
			data := []byte("hello\n")
			switch kind {
			case "zip-range":
				data = z.Bytes()
			case "tar-gz":
				data = gz.Bytes()
			case "tar-bz2":
				data = bz
			}
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("ETag", `"pin"`)
				http.ServeContent(w, r, "source", time.Time{}, bytes.NewReader(data))
			}))
			defer server.Close()
			src := Source{ID: "source", Kind: kind, URL: server.URL, BaseURL: server.URL + "/", Size: int64(len(data)), ETag: "pin", SHA256: digest(data)}
			m := Manifest{Corpus: tc.corpus, Sources: []Source{src}, Files: []File{{Path: "docs/x.txt", Source: "source", Member: "x.txt", Size: 6, SHA256: digest([]byte("hello\n"))}}}
			cache := t.TempDir()
			filename := filepath.Join(cache, m.Corpus, "docs/x.txt")
			stale := []string{m.Corpus + "/docs/.fetch-123", m.Corpus + "/nested/.fetch-456", "dl/.fetch-789"}
			for i, want := range []int{1, 0, 1} {
				if i > 0 {
					for _, name := range stale {
						filename := filepath.Join(cache, name)
						if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filename, []byte("partial"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				if i == 2 {
					if err := os.WriteFile(filename, []byte("jello\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := requests.Load()
				got, err := Fetch(m, cache, ".")
				if err != nil || got.Fetched != want || got.Bytes != int64(want*6) || got.Present != 1-want {
					t.Fatalf("run %d: %+v %v", i, got, err)
				}
				if want == 0 && requests.Load() != before {
					t.Fatal("warm fetch accessed network")
				}
				for _, name := range stale {
					if _, err := os.Stat(filepath.Join(cache, name)); !os.IsNotExist(err) {
						t.Fatalf("temporary file remains: %s: %v", name, err)
					}
				}
			}
			m.Files[0].SHA256 = strings.Repeat("0", 64)
			if _, err := Fetch(m, cache, "."); err == nil || !strings.Contains(err.Error(), "sha256 mismatch: "+m.Corpus+"/docs/x.txt") {
				t.Fatalf("mismatch: %v", err)
			}
			b, err := os.ReadFile(filename)
			if err != nil || string(b) != "hello\n" {
				t.Fatalf("failed fetch replaced destination: %q %v", b, err)
			}
			if kind == "tar-gz" || kind == "tar-bz2" {
				bad := src
				bad.SHA256 = strings.Repeat("0", 64)
				m.Sources[0] = bad
				if _, err := Fetch(m, t.TempDir(), "."); err == nil || !strings.Contains(err.Error(), "sha256 mismatch: source") {
					t.Fatalf("bad archive pin: %v", err)
				}
			}
			if kind == "zip-range" {
				for _, bad := range []Source{func() Source { s := src; s.Size++; return s }(), func() Source { s := src; s.ETag = "changed"; return s }()} {
					m.Sources[0] = bad
					if _, err := Fetch(m, t.TempDir(), "."); err == nil || !strings.Contains(err.Error(), "source") {
						t.Fatalf("bad pin accepted: %v", err)
					}
				}
			}
		})
	}
}

func TestRemoveFetchTemps(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".fetch-123", ".fetch-123.txt", ".fetch-", "extra.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ".fetch-456"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("extra.txt", filepath.Join(root, ".fetch-789")); err != nil {
		t.Fatal(err)
	}
	if err := removeFetchTemps(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".fetch-123", ".fetch-123.txt", ".fetch-", "extra.txt", ".fetch-456", ".fetch-789"} {
		_, err := os.Lstat(filepath.Join(root, name))
		if name == ".fetch-123" && os.IsNotExist(err) {
			continue
		}
		if name == ".fetch-123" || err != nil {
			t.Errorf("%s: unexpected cleanup result: %v", name, err)
		}
	}
}

func TestGitArchive(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	if err := os.WriteFile(filepath.Join(repo, "x.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "x.txt")
	git("-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	commit := git("rev-parse", "HEAD")
	m := Manifest{Corpus: "code", Sources: []Source{{ID: "git", Kind: "git-archive", Ref: "HEAD", Commit: commit}}, Files: []File{{Path: "garp/x.txt", Source: "git", Member: "x.txt", Size: 6, SHA256: digest([]byte("hello\n"))}}}
	if got, err := Fetch(m, t.TempDir(), repo); err != nil || got.Fetched != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	m.Sources[0].Commit = strings.Repeat("0", 40)
	if _, err := Fetch(m, t.TempDir(), repo); err == nil || !strings.Contains(err.Error(), "commit mismatch: git") {
		t.Fatalf("pin: %v", err)
	}
}

func TestRangeReaderRejectsIgnoredRange(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "not a range") }))
	defer s.Close()
	r := rangeReader{source: Source{URL: s.URL, Size: 100}}
	if _, err := r.ReadAt(make([]byte, 4), 0); err == nil {
		t.Fatal("accepted a non-range response")
	}
}
