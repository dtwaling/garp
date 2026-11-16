package systems

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"garp/config"
	"garp/search"
)

// Match search/walk.go's unexported defaultTextMaxBytes for raw text files.
const textMaxBytes = 5 << 20

type document struct {
	path   string
	tf     map[string]int
	length int
}

// Index is an in-memory BM25 index of one manifest subtree. Documents and the
// vocabulary are path/token sorted so floating-point summation is repeatable.
type Index struct {
	docs       []document
	df         map[string]int
	vocabulary []string
	avgdl      float64
}

// BuildIndex indexes only manifestPaths beneath root; both are validated,
// slash-separated corpus-relative paths. Results and Tokens use root-relative
// paths. I/O failures are setup errors. Extraction failures remain as empty
// documents, rather than silently shrinking the judged corpus universe.
func BuildIndex(cacheCorpusDir, root string, manifestPaths []string) (*Index, error) {
	idx := &Index{df: make(map[string]int)}
	registry := search.NewExtractorRegistry()
	paths := unique(manifestPaths)
	sort.Strings(paths)
	for _, p := range paths {
		rel := p
		if root != "." {
			var ok bool
			rel, ok = strings.CutPrefix(p, root+"/")
			if !ok {
				continue
			}
		}
		text, err := indexText(filepath.Join(cacheCorpusDir, filepath.FromSlash(p)), registry)
		if err != nil {
			return nil, fmt.Errorf("index %s: %w", p, err)
		}
		d := document{path: rel, tf: make(map[string]int)}
		for _, token := range tokenize(text) {
			d.tf[token]++
			d.length++
		}
		for token := range d.tf {
			idx.df[token]++
		}
		idx.avgdl += float64(d.length)
		idx.docs = append(idx.docs, d)
	}
	if len(idx.docs) > 0 {
		idx.avgdl /= float64(len(idx.docs))
	}
	for token := range idx.df {
		idx.vocabulary = append(idx.vocabulary, token)
	}
	sort.Strings(idx.vocabulary)
	return idx, nil
}

func indexText(path string, registry *search.ExtractorRegistry) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}
	var text string
	if search.IsBinaryFormat(path) {
		extractor, ok := registry.GetExtractor(filepath.Ext(path))
		if !ok {
			return "", nil
		}
		if ra, ok := extractor.(search.ReaderAtExtractor); ok {
			text, err = ra.ExtractReaderAt(f, info.Size(), search.DefaultMaxExtractedBytes)
		} else {
			var data []byte
			data, err = io.ReadAll(f)
			if err != nil {
				return "", err
			}
			text, err = extractor.ExtractText(data)
		}
		// The engine discards extraction failures; do not index partial text.
		if err != nil {
			text = ""
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(f, textMaxBytes))
		if err != nil {
			return "", err
		}
		text = string(data)
	}
	if config.IsLiteralTextFile(path) {
		return search.CleanContentCode(text), nil
	}
	return search.CleanContent(text), nil
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// Tokens returns a copy of a document's token set for mechanical gold checks.
func (idx *Index) Tokens(path string) map[string]bool {
	out := make(map[string]bool)
	i := sort.Search(len(idx.docs), func(i int) bool { return idx.docs[i].path >= path })
	if i < len(idx.docs) && idx.docs[i].path == path {
		for token := range idx.docs[i].tf {
			out[token] = true
		}
	}
	return out
}

func (idx *Index) queryTokens(terms []string) []string {
	var tokens []string
	for _, term := range terms {
		prefix, wildcard := strings.CutSuffix(term, "*")
		parts := tokenize(prefix)
		for i, token := range parts {
			if wildcard && i == len(parts)-1 {
				for j := sort.SearchStrings(idx.vocabulary, token); j < len(idx.vocabulary) && strings.HasPrefix(idx.vocabulary[j], token); j++ {
					tokens = append(tokens, idx.vocabulary[j])
				}
			} else {
				tokens = append(tokens, token)
			}
		}
	}
	return tokens
}

func (idx *Index) score(d document, tokens []string) float64 {
	if idx.avgdl == 0 {
		return 0
	}
	const k1, b = 1.2, 0.75
	var score float64
	for _, token := range tokens {
		tf := float64(d.tf[token])
		if tf == 0 {
			continue
		}
		df := float64(idx.df[token])
		idf := math.Log(1 + (float64(len(idx.docs))-df+0.5)/(df+0.5))
		score += idf * tf * (k1 + 1) / (tf + k1*(1-b+b*float64(d.length)/idx.avgdl))
	}
	return score
}

// Search scores tokenized terms (use Terms to strip query flags), expanding a
// trailing wildcard on the final token of each term. Only positive scores are
// returned, at most 50, with equal scores ordered by root-relative path.
func (idx *Index) Search(terms []string) []string {
	tokens := idx.queryTokens(terms)
	type hit struct {
		path  string
		score float64
	}
	var hits []hit
	for _, d := range idx.docs {
		if score := idx.score(d, tokens); score > 0 {
			hits = append(hits, hit{d.path, score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return hits[i].path < hits[j].path
		}
		return hits[i].score > hits[j].score
	})
	var ranking []string
	for _, h := range hits[:min(50, len(hits))] {
		ranking = append(ranking, h.path)
	}
	return ranking
}
