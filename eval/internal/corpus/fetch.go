package corpus

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 2 * time.Minute}

// FetchStats counts materialized file bytes, not HTTP headers or archive overhead.
type FetchStats struct {
	Fetched, Present int
	Bytes            int64
}

type entry struct {
	name string
	size int64
	open func() (io.ReadCloser, error)
}

// Fetch repairs missing or corrupt manifest files atomically. The cache is a
// caller-owned directory, not a directory shared with untrusted writers.
func Fetch(m Manifest, cache, repo string) (FetchStats, error) {
	var stats FetchStats
	sources := make(map[string]Source)
	for _, s := range m.Sources {
		if !fs.ValidPath(s.ID) || strings.ContainsAny(s.ID, "/\\") || s.ID == "." {
			return stats, fmt.Errorf("invalid source id %q", s.ID)
		}
		if _, ok := sources[s.ID]; ok {
			return stats, fmt.Errorf("duplicate source %s", s.ID)
		}
		sources[s.ID] = s
	}
	pending := make(map[string][]File)
	seen := make(map[string]bool)
	for _, f := range m.Files {
		if err := Eligible(m.Corpus, f.Path); err != nil {
			return stats, err
		}
		if seen[f.Path] {
			return stats, fmt.Errorf("duplicate path %s", f.Path)
		}
		seen[f.Path] = true
		if _, ok := sources[f.Source]; !ok {
			return stats, fmt.Errorf("unknown source %s", f.Source)
		}
		if !fs.ValidPath(f.Member) || f.Member == "." || strings.Contains(f.Member, `\`) || f.Size < 0 {
			return stats, fmt.Errorf("invalid member %q", f.Member)
		}
		if matches(filepath.Join(cache, m.Corpus, filepath.FromSlash(f.Path)), f.Size, f.SHA256) {
			stats.Present++
			continue
		}
		pending[f.Source] = append(pending[f.Source], f)
	}
	for _, s := range m.Sources {
		files := pending[s.ID]
		if len(files) == 0 {
			continue
		}
		var entries []entry
		if s.Kind == "raw" {
			for _, f := range files {
				entries = append(entries, rawEntry(s, f.Member, f.Size))
			}
		} else {
			var err error
			entries, _, err = openSource(s, cache, repo)
			if err != nil {
				return stats, fmt.Errorf("source %s: %w", s.ID, err)
			}
		}
		members := make(map[string]entry)
		for _, e := range entries {
			if _, ok := members[e.name]; ok {
				return stats, fmt.Errorf("source %s: duplicate member %s", s.ID, e.name)
			}
			members[e.name] = e
		}
		for _, f := range files {
			e, ok := members[f.Member]
			if !ok {
				return stats, fmt.Errorf("source %s: missing member %s", s.ID, f.Member)
			}
			r, err := e.open()
			if err != nil {
				return stats, fmt.Errorf("%s/%s: %w", m.Corpus, f.Path, err)
			}
			err = store(filepath.Join(cache, m.Corpus, filepath.FromSlash(f.Path)), r, f.Size, f.SHA256, m.Corpus+"/"+f.Path)
			closeErr := r.Close()
			if err != nil {
				return stats, err
			}
			if closeErr != nil {
				return stats, closeErr
			}
			stats.Fetched++
			stats.Bytes += f.Size
		}
	}
	return stats, nil
}

func matches(filename string, size int64, hash string) bool {
	f, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != size {
		return false
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	return err == nil && fmt.Sprintf("%x", h.Sum(nil)) == hash
}

func store(filename string, r io.Reader, size int64, hash, label string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".fetch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, size+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size || fmt.Sprintf("%x", h.Sum(nil)) != hash {
		return fmt.Errorf("sha256 mismatch: %s", label)
	}
	return os.Rename(f.Name(), filename)
}

func get(address string) (io.ReadCloser, error) {
	resp, err := httpClient.Get(address)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", address, resp.Status)
	}
	return resp.Body, nil
}

func rawEntry(s Source, name string, size int64) entry {
	return entry{name: name, size: size, open: func() (io.ReadCloser, error) {
		// Escape each path component, retaining the manifest's member separators.
		parts := strings.Split(name, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		return get(s.BaseURL + strings.Join(parts, "/"))
	}}
}

// rangeReader keeps one bounded read-ahead window to avoid a request per small
// archive/zip read. Its download count includes that overhead for budget checks.
// It is used sequentially, including the member streams returned by openSource.
type rangeReader struct {
	source     Source
	offset     int64
	buf        []byte
	downloaded int64
}

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative range offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.source.Size {
		return 0, io.EOF
	}
	if off < r.offset || off+int64(len(p)) > r.offset+int64(len(r.buf)) {
		end := min(r.source.Size, off+max(int64(len(p)), 64<<10))
		req, err := http.NewRequest(http.MethodGet, r.source.URL, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end-1))
		req.Header.Set("If-Match", `"`+r.source.ETag+`"`)
		resp, err := httpClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		want := fmt.Sprintf("bytes %d-%d/%d", off, end-1, r.source.Size)
		if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != want {
			return 0, fmt.Errorf("invalid range response: %s %q (want %q)", resp.Status, resp.Header.Get("Content-Range"), want)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, end-off+1))
		r.downloaded += int64(len(b))
		if err != nil {
			return 0, err
		}
		if int64(len(b)) != end-off {
			return 0, io.ErrUnexpectedEOF
		}
		r.offset = off
		r.buf = b
	}
	n := copy(p, r.buf[off-r.offset:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// openSource catalogs pinned archives. ZIP members remain range-backed; small
// verified tar sources are held in memory. Only regular tar members are exposed,
// never archive paths, modes, symlinks, or hardlinks written onto the filesystem.
// downloadBytes reports the cold HTTP payload budget, even for cached tarballs.
func openSource(s Source, cache, repo string) (entries []entry, downloadBytes func() int64, err error) {
	downloadBytes = func() int64 { return s.Size }
	if s.Kind == "zip-range" {
		resp, e := httpClient.Head(s.URL)
		if e != nil {
			return nil, nil, e
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.ContentLength != s.Size || strings.Trim(resp.Header.Get("ETag"), `"`) != s.ETag {
			return nil, nil, fmt.Errorf("size/etag mismatch: %s", s.ID)
		}
		r := &rangeReader{source: s}
		z, e := zip.NewReader(r, s.Size)
		if e != nil {
			return nil, nil, e
		}
		for _, f := range z.File {
			if f.Mode().IsRegular() {
				entries = append(entries, entry{name: f.Name, size: int64(f.UncompressedSize64), open: f.Open})
			}
		}
		return entries, func() int64 { return r.downloaded }, nil
	}
	var reader io.Reader
	if s.Kind == "git-archive" {
		out, e := exec.Command("git", "-C", repo, "rev-parse", "--verify", s.Ref+"^{commit}").Output()
		if e != nil {
			return nil, nil, e
		}
		if strings.TrimSpace(string(out)) != s.Commit {
			return nil, nil, fmt.Errorf("commit mismatch: %s", s.ID)
		}
		data, e := exec.Command("git", "-C", repo, "archive", s.Commit).Output()
		if e != nil {
			return nil, nil, e
		}
		reader = bytes.NewReader(data)
		downloadBytes = func() int64 { return 0 }
	} else if s.Kind == "tar-bz2" || s.Kind == "tar-gz" {
		filename := filepath.Join(cache, "dl", s.ID)
		if !matches(filename, s.Size, s.SHA256) {
			r, e := get(s.URL)
			if e != nil {
				return nil, nil, e
			}
			e = store(filename, r, s.Size, s.SHA256, s.ID)
			r.Close()
			if e != nil {
				return nil, nil, e
			}
		}
		f, e := os.Open(filename)
		if e != nil {
			return nil, nil, e
		}
		defer f.Close()
		if s.Kind == "tar-bz2" {
			reader = bzip2.NewReader(f)
		} else {
			gz, e := gzip.NewReader(f)
			if e != nil {
				return nil, nil, e
			}
			defer gz.Close()
			reader = gz
		}
	} else {
		return nil, nil, fmt.Errorf("unknown source kind %q", s.Kind)
	}
	tr := tar.NewReader(reader)
	top := ""
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, nil, e
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := h.Name
		if s.Kind == "tar-gz" {
			prefix, tail, ok := strings.Cut(name, "/")
			if !ok || tail == "" {
				return nil, nil, fmt.Errorf("missing archive root: %s", name)
			}
			if top != "" && top != prefix {
				return nil, nil, fmt.Errorf("multiple archive roots")
			}
			top = prefix
			name = tail
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			return nil, nil, e
		}
		entries = append(entries, entry{name: name, size: h.Size, open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }})
	}
	return entries, downloadBytes, nil
}
