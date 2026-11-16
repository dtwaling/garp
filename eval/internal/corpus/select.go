package corpus

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const govdocsURL = "https://digitalcorpora.s3.amazonaws.com/corpora/files/govdocs1/"
const poiURL = "https://raw.githubusercontent.com/apache/poi/47dcff769bb395034dddfd59c4609659b6dbc6a4/test-data/hsmf/"

// Explicit, content-blind list: first 25 name-sorted .msg files <= 512 KiB,
// excluding clusterfuzz-*, from the GitHub contents API at the pinned commit.
// Selection never invokes garp or inspects document text.
var poiNames = []string{
	"51873.msg", "53784_fails.msg", "53784_succeeds.msg",
	"58214_extracted_attachment.msg", "58214_with_attachment.msg",
	"ASCII_CP1251_LCID1049.msg", "ASCII_UTF-8_CP1252_LCID1031.msg",
	"ASCII_UTF-8_CP1252_LCID1031_HTML.msg", "HTMLBodyBinary_CP1251.msg",
	"HTMLBodyBinary_UTF-8.msg", "attachment_msg_inlineImg.msg",
	"attachment_msg_pdf.msg", "attachment_test_msg.msg", "blank.msg",
	"bug66335.msg", "chinese-traditional.msg", "cyrillic_message.msg",
	"example_received_regular.msg", "example_received_unicode.msg",
	"example_sent_regular.msg", "example_sent_unicode.msg", "keywords.msg",
	"logsat.com_signatures_valid.msg", "message_1979.msg", "message_1980.msg",
}

func pinnedSources(corpus string) ([]Source, error) {
	switch corpus {
	case "docs":
		return []Source{
			{ID: "govdocs-thread0", Kind: "zip-range", URL: govdocsURL + "threads/thread0.zip", Size: 322469174, ETag: "853266986a1d22c5bc4c5652dac7bb02-39"},
			{ID: "govdocs-docx", Kind: "zip-range", URL: govdocsURL + "by_type/docx.zip", Size: 29033269, ETag: "653527964512c31378d36680cb666a47-4"},
			{ID: "govdocs-rtf", Kind: "zip-range", URL: govdocsURL + "by_type/rtf.zip", Size: 67865288, ETag: "e031aa41946310dd8237e4c9682e95ad-9"},
			{ID: "sa-easy-ham", Kind: "tar-bz2", URL: "https://spamassassin.apache.org/old/publiccorpus/20030228_easy_ham.tar.bz2", Size: 1612216, SHA256: "2b7b65904bcfcc31d2b5f51946f2d261370b257402cbbd62930b46ab83367438"},
			{ID: "poi-hsmf", Kind: "raw", BaseURL: poiURL},
		}, nil
	case "code":
		return []Source{
			{ID: "ripgrep", Kind: "tar-gz", URL: "https://codeload.github.com/BurntSushi/ripgrep/tar.gz/0e8390a66fbcf6eeac1aeb0541b367663a597c79", Size: 582628, SHA256: "7d62d1c0bac59f5175bef81b256f844559d5f91a750fd3ea45bcde5fe0ff4daa", Ref: "14.1.1", Commit: "4649aa9700619f94cf9c66876e9549d83420e16c"},
			{ID: "garp-v0.10.0", Kind: "git-archive", Ref: "v0.10.0", Commit: "f85a90c24d69e5febf829961c29658d73edfb01c"},
		}, nil
	default:
		return nil, fmt.Errorf("unknown corpus %q", corpus)
	}
}

func selectionCounts(source string) map[string]int {
	switch source {
	case "govdocs-thread0":
		return map[string]int{".pdf": 160, ".doc": 40, ".txt": 40, ".html": 25, ".xml": 10, ".csv": 10}
	case "govdocs-docx":
		return map[string]int{".docx": 30}
	case "govdocs-rtf":
		return map[string]int{".rtf": 30}
	case "poi-hsmf":
		return map[string]int{".msg": 25}
	}
	return nil
}

func selectEntries(corpus string, entries []entry, root string, counts map[string]int) []entry {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	used := make(map[string]int)
	var selected []entry
	for _, e := range entries {
		if Eligible(corpus, path.Join(root, e.name)) != nil {
			continue
		}
		if corpus == "docs" {
			ext := strings.ToLower(path.Ext(e.name))
			if e.size > 512<<10 || used[ext] >= counts[ext] {
				continue
			}
			used[ext]++
		}
		selected = append(selected, e)
	}
	return selected
}

func checkBudget(corpus string, count int, size, download int64) error {
	if corpus == "docs" && (count > 450 || size > 50_000_000 || download > 90_000_000) {
		return fmt.Errorf("docs budget exceeded: %d files, %d bytes on disk, %d download bytes", count, size, download)
	}
	if corpus == "code" && size > 10_000_000 {
		return fmt.Errorf("code budget exceeded: %d bytes on disk", size)
	}
	return nil
}

// Select regenerates a manifest from the fixed, content-blind selection rule.
// It hashes original bytes only; it never extracts text or runs search systems.
// The budget includes range read-ahead and archive metadata, not HTTP headers.
func Select(corpus, cache, repo string) (Manifest, error) {
	sources, err := pinnedSources(corpus)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Corpus: corpus, Sources: sources}
	var size, download int64
	seen := make(map[string]bool)
	for _, s := range sources {
		var entries []entry
		var downloaded func() int64
		root := "govdocs"
		if corpus == "code" {
			root = "ripgrep"
			if s.Kind == "git-archive" {
				root = "garp"
			}
		}
		if s.Kind == "raw" {
			root = "msg"
			for _, name := range poiNames {
				entries = append(entries, rawEntry(s, name, 0))
			}
			downloaded = func() int64 { return 0 }
		} else {
			entries, downloaded, err = openSource(s, cache, repo)
			if err != nil {
				return m, fmt.Errorf("source %s: %w", s.ID, err)
			}
		}
		selected := selectEntries(corpus, entries, root, selectionCounts(s.ID))
		if s.Kind == "tar-bz2" {
			root = "mail"
			slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
			selected = nil
			for _, e := range entries {
				number, _, ok := strings.Cut(path.Base(e.name), ".")
				if !ok || len(number) != 5 || e.size > 512<<10 {
					continue
				}
				if _, err := strconv.Atoi(number); err != nil {
					continue
				}
				selected = append(selected, e)
				if len(selected) == 60 {
					break
				}
			}
		}
		for _, e := range selected {
			name := e.name
			if corpus == "docs" {
				name = path.Base(name)
			}
			if root == "mail" {
				number, _, _ := strings.Cut(name, ".")
				name = "sa-" + number + ".eml"
			}
			target := path.Join(root, name)
			if err := Eligible(corpus, target); err != nil {
				return m, err
			}
			if seen[target] {
				return m, fmt.Errorf("duplicate selected path %s", target)
			}
			seen[target] = true
			r, err := e.open()
			if err != nil {
				return m, fmt.Errorf("%s: %w", target, err)
			}
			limit := int64(10_000_000)
			if corpus == "docs" {
				limit = 512 << 10
			}
			h := sha256.New()
			n, err := io.Copy(h, io.LimitReader(r, limit+1))
			closeErr := r.Close()
			if err != nil {
				return m, err
			}
			if closeErr != nil {
				return m, closeErr
			}
			if n > limit || (s.Kind != "raw" && n != e.size) {
				return m, fmt.Errorf("size mismatch: %s", target)
			}
			m.Files = append(m.Files, File{Path: target, Source: s.ID, Member: e.name, Size: n, SHA256: fmt.Sprintf("%x", h.Sum(nil))})
			size += n
			if s.Kind == "raw" {
				download += n
			}
		}
		download += downloaded()
		if err := checkBudget(corpus, len(m.Files), size, download); err != nil {
			return m, err
		}
	}
	slices.SortFunc(m.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return m, nil
}

// WriteManifest uses stable struct field order, path order, and a final newline.
func WriteManifest(dir string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, m.Corpus+".json"), append(b, '\n'), 0644)
}
