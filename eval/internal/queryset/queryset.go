// Package queryset loads and validates the harness's judged query sets.
package queryset

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"garp/search"
)

type Set struct {
	Corpus      string  `json:"corpus"`
	TokenBudget int     `json:"token_budget"`
	Queries     []Query `json:"queries"`
}

type Query struct {
	ID                string   `json:"id"`
	Root              string   `json:"root"`
	Kind              string   `json:"kind"`
	Need              string   `json:"need"`
	Args              []string `json:"args"`
	Gold              []Gold   `json:"gold"`
	JudgedNonrelevant []string `json:"judged_nonrelevant"`
}

// Gold names a root-relative file and optionally an inclusive 1-based range.
// A slice preserves malformed JSON range lengths for Validate to reject,
// rather than silently truncating them as encoding/json does for arrays.
type Gold struct {
	File  string `json:"file"`
	Lines []int  `json:"lines,omitempty"`
}

// Minimums are query counts, not gold-item counts. Zero disables a minimum.
type Minimums struct {
	Queries, Proximity, Synonym, Single int
	DocsPerFormat, Garp, Ripgrep        int
}

func DesignMinimums() Minimums {
	return Minimums{Queries: 40, Proximity: 12, Synonym: 10, Single: 10, DocsPerFormat: 4, Garp: 15, Ripgrep: 20}
}

// Load returns the parsed query set and sha256 of the exact file bytes.
// Call Validate before executing queries; Load only checks JSON decoding.
func Load(filename string) (Set, string, error) {
	var s Set
	b, err := os.ReadFile(filename)
	if err != nil {
		return s, "", err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, "", fmt.Errorf("query set %s: %w", filename, err)
	}
	return s, fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

var termPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+\*?$`)

// Leading '-' is flag syntax in garp, even when it matches the term grammar.
// Only the listed spellings are allowed; numeric values follow garp's positive
// integer contract. Harness-owned options must never reach the term fallback.
func validateArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--strict", "--partial", "--partial=prefix", "--partial=contains", "--smart-forms":
		case "--distance", "-m":
			if i+1 == len(args) {
				return fmt.Errorf("%s needs a positive integer", a)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return fmt.Errorf("%s needs a positive integer, got %q", a, args[i])
			}
		default:
			if strings.HasPrefix(a, "-") || !termPattern.MatchString(a) {
				return fmt.Errorf("disallowed token %q", a)
			}
		}
	}
	return nil
}

func relativePath(p string) bool {
	return fs.ValidPath(p) && !strings.Contains(p, `\`)
}

// Validate checks syntax, manifest membership, line ranges and minimum counts.
// Manifest paths are corpus-relative; gold and nonrelevant paths are relative
// to each query's root. Mechanical kind checks against extracted content are
// deliberately deferred to the runner after it builds the corpus index.
// Set-wide failures name all query IDs (or <empty> for an empty set).
func Validate(s Set, manifestPaths []string, minimums Minimums) []error {
	var errs []error
	add := func(id, message string, args ...any) {
		errs = append(errs, fmt.Errorf("query %s: %s", id, fmt.Sprintf(message, args...)))
	}
	paths := make(map[string]bool, len(manifestPaths))
	for _, p := range manifestPaths {
		paths[p] = true
	}
	seen := make(map[string]bool)
	kinds, roots, formats := make(map[string]int), make(map[string]int), make(map[string]int)
	ids := make([]string, 0, len(s.Queries))
	for _, q := range s.Queries {
		ids = append(ids, q.ID)
		if q.ID == "" {
			add("<empty>", "id is required")
		} else if seen[q.ID] {
			add(q.ID, "duplicate id")
		}
		seen[q.ID] = true
		if !relativePath(q.Root) {
			add(q.ID, "invalid root %q", q.Root)
		}
		switch q.Kind {
		case "proximity", "synonym", "single":
		default:
			add(q.ID, "unknown kind %q", q.Kind)
		}
		kinds[q.Kind]++
		roots[q.Root]++
		if err := validateArgs(q.Args); err != nil {
			add(q.ID, "args: %v", err)
		}
		checkPath := func(p string) {
			if !relativePath(p) || p == "." {
				add(q.ID, "invalid file path %q", p)
				return
			}
			if !paths[path.Join(q.Root, p)] {
				add(q.ID, "path %q under root %q is not in manifest", p, q.Root)
			}
		}
		if len(q.Gold) == 0 {
			add(q.ID, "gold must contain at least one item")
		}
		queryFormats := make(map[string]bool)
		for _, g := range q.Gold {
			checkPath(g.File)
			if g.Lines != nil {
				if search.IsBinaryFormat(g.File) {
					add(q.ID, "lines on binary format %q", g.File)
				}
				if len(g.Lines) != 2 || g.Lines[0] < 1 || g.Lines[1] < g.Lines[0] {
					add(q.ID, "invalid lines for %q: %v", g.File, g.Lines)
				}
			}
			queryFormats[strings.TrimPrefix(strings.ToLower(path.Ext(g.File)), ".")] = true
		}
		for ext := range queryFormats {
			formats[ext]++
		}
		for _, p := range q.JudgedNonrelevant {
			checkPath(p)
		}
	}
	allIDs := strings.Join(ids, ", ")
	if allIDs == "" {
		allIDs = "<empty>"
	}
	if s.TokenBudget <= 0 {
		add(allIDs, "token_budget must be positive")
	}
	if s.Corpus != "docs" && s.Corpus != "code" {
		add(allIDs, "unknown corpus %q", s.Corpus)
	}
	checkMinimum := func(label string, got, want int) {
		if got < want {
			add(allIDs, "%s: got %d queries, need at least %d", label, got, want)
		}
	}
	checkMinimum("queries", len(s.Queries), minimums.Queries)
	checkMinimum("proximity", kinds["proximity"], minimums.Proximity)
	checkMinimum("synonym", kinds["synonym"], minimums.Synonym)
	checkMinimum("single", kinds["single"], minimums.Single)
	if s.Corpus == "docs" {
		for _, ext := range []string{"pdf", "doc", "docx", "rtf", "eml", "msg"} {
			checkMinimum(ext, formats[ext], minimums.DocsPerFormat)
		}
	} else if s.Corpus == "code" {
		checkMinimum("garp", roots["garp"], minimums.Garp)
		checkMinimum("ripgrep", roots["ripgrep"], minimums.Ripgrep)
	}
	return errs
}
