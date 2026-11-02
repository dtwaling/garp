package app

// TODO: Add support for --only <type> (e.g., --only pdf)
// - CLI parsing: parse and store an "onlyType" (single extension) mutually exclusive with --code and extension excludes.
// - Discovery: restrict file type globs to only that extension when set.
// - UI: reflect the constraint in the "Target" header line to stay truthful.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"garp/config"
	"garp/search"
)

var version = "0.9.2"

// Arguments for CLI flags (used to seed TUI)
type Arguments struct {
	SearchWords        []string
	ExcludeWords       []string
	IncludeCode        bool
	Strict             bool
	SmartForms         bool
	Distance           int
	HeavyConcurrency   int
	FilterWorkers      int
	FileTimeoutBinary  int
	MaxExcerpts        int
	MaxResults         int
	MaxResultsProvided bool
	MaxResultsErr      error // non-nil if --max-results validation failed
	OnlyType           string
	Partial            search.PartialMode
	PartialErr         error // non-nil if --partial validation failed

	// StartDir: base directory for file walks (--startdir flag).
	// Empty string means use the current working directory.
	StartDir    string
	StartDirErr error // non-nil if validation failed

	// PathScope: list of simple glob patterns to restrict file walks (--pathscope flag).
	// Empty slice means no restriction.
	PathScope    []string
	PathScopeErr error // non-nil if validation failed

	// NoIgnore disables repository ignore-file evaluation.
	NoIgnore bool

	// MaxFileSize is zero unless --max-filesize was explicitly provided.
	MaxFileSize    int64
	MaxFileSizeErr error // non-nil if validation failed

	// PlainOutput: when true, skip the TUI and emit clean line-oriented text to stdout.
	// Designed for programmatic callers (MCP tools, scripts) that need machine-readable output.
	PlainOutput bool

	// JSONOutput: when true, skip the TUI and emit a JSON document to stdout.
	// Preferred over --plain for agent/MCP callers -- unambiguous structure, no parse fragility.
	JSONOutput bool

	// PathOnly: when true, skip the TUI and emit only distinct matching file paths (one per line) to stdout.
	PathOnly bool
}

// parseArguments parses command line args
func parseArguments(args []string) *Arguments {
	result := &Arguments{
		SearchWords:       []string{},
		ExcludeWords:      []string{},
		IncludeCode:       false,
		Distance:          0,
		HeavyConcurrency:  2,
		FilterWorkers:     4,
		FileTimeoutBinary: 1000,
		MaxExcerpts:       1,
	}

	parsingExcludes := false
	expectDistance := false
	expectHeavy := false
	expectTimeout := false
	expectWorkers := false
	expectMaxExcerpts := false
	expectMaxResults := false
	expectOnly := false
	expectStartDir := false
	expectPathScope := false
	expectMaxFileSize := false
	heavyProvided := false

	for _, a := range args {
		if expectDistance {
			if n, err := strconv.Atoi(a); err == nil && n > 0 {
				result.Distance = n
			}
			expectDistance = false
			continue
		}
		if expectHeavy {
			if n, err := strconv.Atoi(a); err == nil && n > 0 {
				result.HeavyConcurrency = n
				heavyProvided = true
			}
			expectHeavy = false
			continue
		}
		if expectTimeout {
			if n, err := strconv.Atoi(a); err == nil && n > 0 {
				result.FileTimeoutBinary = n
			}
			expectTimeout = false
			continue
		}
		if expectWorkers {
			if n, err := strconv.Atoi(a); err == nil && n > 0 {
				result.FilterWorkers = n
			}
			expectWorkers = false
			continue
		}
		if expectMaxExcerpts {
			if n, err := strconv.Atoi(a); err == nil && n > 0 {
				if n > 50 {
					n = 50
				}
				result.MaxExcerpts = n
			}
			expectMaxExcerpts = false
			continue
		}
		if expectMaxResults {
			result.MaxResultsErr = nil
			n, err := strconv.Atoi(a)
			switch {
			case err != nil:
				result.MaxResultsErr = fmt.Errorf("--max-results: invalid integer %q", a)
			case n < 0:
				result.MaxResultsErr = fmt.Errorf("--max-results: value must be non-negative")
			default:
				result.MaxResults = n
			}
			expectMaxResults = false
			continue
		}
		if expectOnly {
			result.OnlyType = strings.TrimPrefix(strings.ToLower(a), ".")
			expectOnly = false
			continue
		}
		if expectStartDir {
			cleaned, err := search.ValidateStartDir(a)
			if err != nil {
				result.StartDirErr = err
			} else {
				result.StartDir = cleaned
			}
			expectStartDir = false
			continue
		}
		if expectPathScope {
			segments, err := search.ValidatePathScope(a)
			if err != nil {
				result.PathScopeErr = err
			} else {
				result.PathScope = segments
			}
			expectPathScope = false
			continue
		}
		if expectMaxFileSize {
			size, err := search.ParseMaxFileSize(a)
			result.MaxFileSize = size
			result.MaxFileSizeErr = err
			expectMaxFileSize = false
			continue
		}
		switch a {
		case "--code":
			result.IncludeCode = true
		case "--strict":
			result.Strict = true
		case "--not":
			parsingExcludes = true
		case "--distance", "-distance":
			expectDistance = true
		case "--heavy-concurrency":
			expectHeavy = true
		case "--file-timeout-binary":
			expectTimeout = true
		case "--workers", "-workers":
			expectWorkers = true
		case "--max-excerpts":
			expectMaxExcerpts = true
		case "--max-results":
			result.MaxResultsProvided = true
			expectMaxResults = true
		case "--only":
			expectOnly = true
		case "--startdir":
			expectStartDir = true
		case "--pathscope":
			expectPathScope = true
		case "--no-ignore":
			result.NoIgnore = true
		case "--max-filesize":
			expectMaxFileSize = true
		case "--smart-forms":
			result.SmartForms = true
		case "--partial":
			result.Partial = search.PartialModePrefix
		case "--plain":
			result.PlainOutput = true
		case "--json":
			result.JSONOutput = true
		case "--pathonly":
			result.PathOnly = true
		case "--help", "-h":
			showUsage()
			os.Exit(0)
		case "--version", "-v":
			showVersion()
			os.Exit(0)
		default:
			if strings.HasPrefix(a, "--partial=") {
				switch strings.TrimPrefix(a, "--partial=") {
				case "off":
					result.Partial = search.PartialModeOff
				case string(search.PartialModePrefix):
					result.Partial = search.PartialModePrefix
				case string(search.PartialModeContains):
					result.Partial = search.PartialModeContains
				default:
					result.PartialErr = fmt.Errorf("invalid --partial mode %q; supported modes: prefix, contains, off", strings.TrimPrefix(a, "--partial="))
				}
			} else if parsingExcludes {
				result.ExcludeWords = append(result.ExcludeWords, a)
			} else {
				result.SearchWords = append(result.SearchWords, a)
			}
		}
	}
	if expectMaxFileSize {
		result.MaxFileSize = 0
		result.MaxFileSizeErr = fmt.Errorf("--max-filesize: missing value")
	}
	if expectMaxResults {
		result.MaxResultsErr = fmt.Errorf("--max-results: missing value")
	}
	if expectStartDir {
		result.StartDirErr = fmt.Errorf("--startdir: missing value")
	}
	result.SearchWords = search.NormalizeQueryTerms(result.SearchWords)

	// Auto-derive HeavyConcurrency when not explicitly provided: base on workers and available RAM
	if !heavyProvided {
		// default derived heavy = max(1, workers/2)
		workers := result.FilterWorkers
		derived := workers / 2
		if derived < 1 {
			derived = 1
		}
		// Derive an upper bound from system RAM (~ one heavy slot per 4 GiB), clamp to [1,8]
		memAvailKB := int64(0)
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			lines := strings.Split(string(b), "\n")
			// Prefer MemAvailable; fallback to MemFree if unavailable
			for _, ln := range lines {
				if strings.HasPrefix(ln, "MemAvailable:") {
					fields := strings.Fields(ln)
					if len(fields) >= 2 {
						if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
							memAvailKB = kb
						}
					}
					break
				}
			}
			if memAvailKB == 0 {
				for _, ln := range lines {
					if strings.HasPrefix(ln, "MemFree:") {
						fields := strings.Fields(ln)
						if len(fields) >= 2 {
							if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
								memAvailKB = kb
							}
						}
						break
					}
				}
			}
		}
		memAvailGB := memAvailKB / (1024 * 1024)
		ramBound := int(memAvailGB / 4)
		if ramBound < 1 {
			ramBound = 1
		}
		if derived > ramBound {
			derived = ramBound
		}
		if derived > 8 {
			derived = 8
		}
		result.HeavyConcurrency = derived
	}
	return result
}

func resolveMaxResults(args *Arguments) int {
	if args.MaxResultsProvided {
		return args.MaxResults
	}
	if args.PathOnly {
		return 0
	}
	if args.JSONOutput || args.PlainOutput {
		return 50
	}
	return 0
}

// showUsage (styled)
func showUsage() {
	fmt.Println()
	// Styled CLI help matching the TUI theme
	logoTop := " █▀▀ ▄▀█ █▀█ █▀█"
	logoBottom := fmt.Sprintf(" █▄█ █▀█ █▀▄ █▀▀  v%s", version)
	// Pad lines to equal width and render left-aligned to avoid odd spacing
	if len(logoTop) < len(logoBottom) {
		logoTop += strings.Repeat(" ", len(logoBottom)-len(logoTop))
	} else if len(logoBottom) < len(logoTop) {
		logoBottom += strings.Repeat(" ", len(logoTop)-len(logoBottom))
	}
	fmt.Println(lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Render(logoTop + "\n" + logoBottom))
	fmt.Println()

	// Usage
	fmt.Println(subHeaderStyle.Render("USAGE"))
	fmt.Println(infoStyle.Render(wrapTextWithIndent("  garp ", "[--code] [--strict] [--partial[=MODE]] [--distance N] [--max-excerpts N] [--max-results N]\n[--startdir PATH] [--pathscope PATTERNS] [--no-ignore] [--max-filesize SIZE]\n[--pathonly] [--plain] [--json] [--heavy-concurrency N] [--workers N]\n[--file-timeout-binary N] <word1> <word2> ...\n[--not <exclude1> <exclude2> ...]", 100)))
	fmt.Println()

	// Flags
	fmt.Println(subHeaderStyle.Render("FLAGS"))
	fmt.Println(infoStyle.Render("  --code                  Include code files in the search"))
	fmt.Println(infoStyle.Render("  --strict                Require all search terms to match"))
	fmt.Println(infoStyle.Render("  --partial[=MODE]        Enable partial word matching (prefix, contains)."))
	fmt.Println(infoStyle.Render("                          Default mode: prefix. When omitted: whole-word."))
	fmt.Println(infoStyle.Render("                          Tip: append * to a term (e.g. deploy*) for per-term prefix."))
	fmt.Println(infoStyle.Render("  --distance N            Proximity window in characters (default 5000)"))
	fmt.Println(infoStyle.Render("  --max-excerpts N        Maximum excerpts per file; >=90% new content (up to 10% overlap; default 1, max 50)"))
	fmt.Println(infoStyle.Render("  --max-results N         Maximum matching files; 0 = unlimited."))
	fmt.Println(infoStyle.Render("                          Defaults: --json 50, --plain 50, --pathonly 0, TUI 0."))
	fmt.Println(infoStyle.Render("  --heavy-concurrency N   Concurrent heavy extractions (auto if omitted)"))
	fmt.Println(infoStyle.Render("  --workers N             Stage 2 text filter workers (default 2)"))
	fmt.Println(infoStyle.Render("  --file-timeout-binary N Timeout in ms for binary extraction (default 1000)"))
	fmt.Println(infoStyle.Render("  --smart-forms          Enable smart word forms (s, es, ed, ing, al, tion/ation)"))
	fmt.Println(infoStyle.Render("  --only <type>          Search only a single file type (e.g., pdf); ignores --code"))
	fmt.Println(infoStyle.Render("  --startdir <path>      Base directory to search (default: current directory)"))
	fmt.Println(infoStyle.Render("                         Accepts Linux, macOS, or Windows paths; quote if path has spaces"))
	fmt.Println(infoStyle.Render("                         Wildcards and shell metacharacters are rejected"))
	fmt.Println(infoStyle.Render("  --pathscope <patterns> Comma-separated directory patterns to restrict the search walk."))
	fmt.Println(infoStyle.Render("                         Directories outside the scope are pruned before any files are visited --"))
	fmt.Println(infoStyle.Render("                         not filtered after the fact. Use this to exclude large dirs like .venv,"))
	fmt.Println(infoStyle.Render("                         node_modules, or any subtree you don't want walked at all."))
	fmt.Println(infoStyle.Render("                         Wildcards: * (any chars) and ? (single char) only."))
	fmt.Println(infoStyle.Render("                         Trailing slash optional: 'audio2midi/' and 'audio2midi' both work."))
	fmt.Println(infoStyle.Render("                         Do not include file extensions (use --not / --only for that)."))
	fmt.Println(infoStyle.Render("                         Example: --pathscope='audio2midi/,docs/,tests/*'"))
	fmt.Println(infoStyle.Render("  --no-ignore            Do not apply repository ignore files (.gitignore, .ignore, or exclude)."))
	fmt.Println(infoStyle.Render("  --max-filesize SIZE    Maximum file size (B, K/KB/KiB, M/MB/MiB; maximum 100M)."))
	fmt.Println(infoStyle.Render("  --pathonly             Skip the interactive TUI; emit only distinct matching file paths (one per line)."))
	fmt.Println(infoStyle.Render("  --plain                Skip the interactive TUI; emit plain line-oriented output to stdout."))
	fmt.Println(infoStyle.Render("                         Useful for scripting and MCP tool callers that need machine-readable results."))
	fmt.Println(infoStyle.Render("  --json                 Skip the interactive TUI; emit a JSON document to stdout."))
	fmt.Println(infoStyle.Render("                         Preferred over --plain for agent/MCP callers -- unambiguous, no parse fragility."))
	fmt.Println(infoStyle.Render("  --not ...               Tokens after this are exclusions;"))
	fmt.Println(infoStyle.Render("                          extensions starting with '.' exclude types; others exclude words"))
	fmt.Println(infoStyle.Render("  --help, -h              Show help"))
	fmt.Println(infoStyle.Render("  --version, -v           Show version"))
	fmt.Println()

	// Examples
	fmt.Println(subHeaderStyle.Render("EXAMPLES"))
	fmt.Println(infoStyle.Render("  garp contract payment agreement"))
	fmt.Println(infoStyle.Render("  garp contract payment agreement --distance 200"))
	fmt.Println(infoStyle.Render("  garp mutex changed --code"))
	fmt.Println(infoStyle.Render("  garp bank wire update --not .txt test"))
	fmt.Println(infoStyle.Render("  garp approval crypto gemini --smart-forms"))
	fmt.Println(infoStyle.Render("  garp report earnings --only pdf"))
	fmt.Println()
}

// showVersion
func showVersion() {
	// successStyle is provided in tui.go (same package).
	fmt.Println(successStyle.Render("garp v" + version))
}

// ansiEscRe and runJSON/runPlain now read RawExcerpts (pre-highlight) directly from SearchResult,
// so no ANSI stripping is needed. This var is intentionally removed.

// jsonExcerpt is a single matched chunk with its 1-based source start line
// and per-chunk ranked quality. start_line is omitted when unknown (0) --
// e.g. binary/extracted formats (PDF, DOCX, email) which have no stable
// source lines. score/term_count are omitted when the chunk did not frame
// a ranked window (fallback paths).
type jsonExcerpt struct {
	Text      string `json:"text"`
	StartLine int    `json:"start_line,omitempty"`
	Score     int    `json:"score,omitempty"`
	TermCount int    `json:"term_count,omitempty"`
}

// jsonResult is a single file match in --json output.
type jsonResult struct {
	File         string        `json:"file"`
	SizeB        int64         `json:"size_bytes"`
	Score        int           `json:"score"`
	TermCount    int           `json:"term_count"`
	MatchedTerms []string      `json:"matched_terms"`
	SpanLength   int           `json:"span_length,omitempty"`
	Excerpts     []jsonExcerpt `json:"excerpts"`
}

// excerptStartLineAt returns the start line for excerpt index i from the parallel
// line slice on a SearchResult, tolerating short/nil slices (returns 0 when unknown).
func excerptStartLineAt(starts []int, i int) int {
	if i >= 0 && i < len(starts) {
		return starts[i]
	}
	return 0
}

// excerptChunkScoreAt returns the ranked score for excerpt index i from the
// parallel chunk-quality slice on a SearchResult (0 when absent).
func excerptChunkScoreAt(scores []int, i int) int {
	if i >= 0 && i < len(scores) {
		return scores[i]
	}
	return 0
}

// excerptChunkTermCountAt returns the matched-term count for excerpt index i
// from the parallel chunk-quality slice on a SearchResult (0 when absent).
func excerptChunkTermCountAt(counts []int, i int) int {
	if i >= 0 && i < len(counts) {
		return counts[i]
	}
	return 0
}

// formatLine renders a compact "L<start>" label, or "" when unknown.
func formatLine(start int) string {
	if start <= 0 {
		return ""
	}
	return fmt.Sprintf("L%d", start)
}

// jsonOutput is the top-level envelope emitted by --json.
type jsonOutput struct {
	Query   jsonQuery    `json:"query"`
	Matches int          `json:"matches"`
	Results []jsonResult `json:"results"`
}

// jsonQuery echoes the search parameters back to the caller so the response is self-describing.
type jsonQuery struct {
	Terms       []string `json:"terms"`
	Strict      bool     `json:"strict,omitempty"`
	Excludes    []string `json:"excludes,omitempty"`
	Partial     string   `json:"partial,omitempty"`
	StartDir    string   `json:"start_dir,omitempty"`
	PathScope   []string `json:"path_scope,omitempty"`
	OnlyType    string   `json:"only_type,omitempty"`
	Code        bool     `json:"include_code"`
	NoIgnore    bool     `json:"no_ignore,omitempty"`
	MaxFileSize int64    `json:"max_filesize,omitempty"`
}

func nativePathScope(pathScope []string) []string {
	if len(pathScope) == 0 {
		return nil
	}
	native := make([]string, len(pathScope))
	for i, pattern := range pathScope {
		native[i] = filepath.FromSlash(pattern)
	}
	return native
}

// buildFileTypes returns the ripgrep-style -g globs for a search: a single
// --only type when given, otherwise the document (and optionally code) set.
func buildFileTypes(includeCode bool, onlyType string) []string {
	if onlyType != "" {
		return config.OnlyTypeGlobs(onlyType)
	}
	return config.BuildRipgrepFileTypes(includeCode)
}

func walkOptions(args *Arguments) search.WalkOptions {
	return search.WalkOptions{
		NoIgnore:    args.NoIgnore,
		MaxFileSize: args.MaxFileSize,
	}
}

func preflightFileCount(args *Arguments) (int, error) {
	fileTypes := buildFileTypes(args.IncludeCode, args.OnlyType)
	return search.GetDocumentFileCountWithOptions(fileTypes, args.StartDir, args.PathScope, walkOptions(args))
}

// newSilentSearchEngine configures a silent SearchEngine for non-TUI CLI modes
// (--json, --plain, --pathonly).
func newSilentSearchEngine(args *Arguments) *search.SearchEngine {
	fileTypes := buildFileTypes(args.IncludeCode, args.OnlyType)
	se := search.NewSearchEngineWithWorkers(
		args.SearchWords,
		args.ExcludeWords,
		fileTypes,
		args.IncludeCode,
		args.HeavyConcurrency,
		args.FileTimeoutBinary,
		args.FilterWorkers,
	)
	se.Silent = true
	se.Strict = args.Strict
	se.Partial = args.Partial
	se.PathOnly = args.PathOnly
	se.NoIgnore = args.NoIgnore
	se.MaxFileSize = args.MaxFileSize
	if args.Distance > 0 {
		se.Distance = args.Distance
	}
	if args.StartDir != "" {
		se.StartDir = args.StartDir
	}
	if len(args.PathScope) > 0 {
		se.PathScope = args.PathScope
	}
	se.MaxExcerpts = args.MaxExcerpts
	se.MaxResults = resolveMaxResults(args)
	return se
}

// runJSON executes the search without the TUI and writes a JSON document to stdout.
// Errors go to stderr as plain text; the exit code is non-zero on failure.
func runJSON(args *Arguments) int {
	se := newSilentSearchEngine(args)

	results, err := se.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		return 1
	}

	out := jsonOutput{
		Query: jsonQuery{
			Terms:       args.SearchWords,
			Strict:      args.Strict,
			Excludes:    args.ExcludeWords,
			Partial:     string(args.Partial),
			StartDir:    args.StartDir,
			PathScope:   nativePathScope(args.PathScope),
			OnlyType:    args.OnlyType,
			Code:        args.IncludeCode,
			NoIgnore:    args.NoIgnore,
			MaxFileSize: args.MaxFileSize,
		},
		Matches: len(results),
		Results: make([]jsonResult, 0, len(results)),
	}

	for _, r := range results {
		excerpts := make([]jsonExcerpt, len(r.RawExcerpts))
		for i, ex := range r.RawExcerpts {
			excerpts[i] = jsonExcerpt{
				Text:      ex,
				StartLine: excerptStartLineAt(r.StartLines, i),
				Score:     excerptChunkScoreAt(r.ChunkScores, i),
				TermCount: excerptChunkTermCountAt(r.ChunkTermCounts, i),
			}
		}
		out.Results = append(out.Results, jsonResult{
			File:         r.FilePath,
			SizeB:        r.FileSize,
			Score:        r.Score,
			TermCount:    r.TermCount,
			MatchedTerms: r.MatchedTerms,
			SpanLength:   r.SpanLength,
			Excerpts:     excerpts,
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	// Don't HTML-escape <, >, & -- code excerpts (PHP "<?php", Delphi generics, "&&") are
	// far more readable as raw characters, and stdout consumers don't need HTML safety.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "Error encoding JSON: "+err.Error())
		return 1
	}
	return 0
}

// runPlain executes the search without the TUI and writes clean text to stdout.
// Output format:
//
//	MATCH <n>/<total>
//	FILE: <path>
//	SIZE: <bytes>
//	EXCERPT <i> [L<start>]: <text>
//	---
//
// The [L<start>] line is present for text/code files and omitted for binary/extracted
// formats (PDF, DOCX, email) that have no stable source lines.
//
// Zero matches produces a single "NO RESULTS" line.
// Errors go to stderr with no ANSI color.
func runPlain(args *Arguments) int {
	se := newSilentSearchEngine(args)

	results, err := se.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		return 1
	}
	if args.Partial != search.PartialModeOff {
		fmt.Printf("Mode: partial (%s)\n", args.Partial)
	}

	if len(results) == 0 {
		fmt.Println("NO RESULTS")
		return 0
	}

	// Use RawExcerpts (pre-highlight) -- no ANSI stripping needed.
	// Excerpt headers carry per-chunk ranked quality when the chunk framed a
	// ranked window; the old "[L<start>]" form is kept for quality-less chunks.
	for i, r := range results {
		fmt.Printf("MATCH %d/%d [Score: %d (%d/%d terms)]\n", i+1, len(results), r.Score, r.TermCount, len(args.SearchWords))
		fmt.Printf("FILE: %s\n", r.FilePath)
		fmt.Printf("SIZE: %d\n", r.FileSize)
		for j, ex := range r.RawExcerpts {
			score := excerptChunkScoreAt(r.ChunkScores, j)
			terms := excerptChunkTermCountAt(r.ChunkTermCounts, j)
			switch {
			case score > 0 && terms > 0 && formatLine(excerptStartLineAt(r.StartLines, j)) != "":
				fmt.Printf("EXCERPT %d [%s, Score: %d (%d terms)]: %s\n", j+1, formatLine(excerptStartLineAt(r.StartLines, j)), score, terms, ex)
			case score > 0 && terms > 0:
				fmt.Printf("EXCERPT %d [Score: %d (%d terms)]: %s\n", j+1, score, terms, ex)
			case formatLine(excerptStartLineAt(r.StartLines, j)) != "":
				fmt.Printf("EXCERPT %d [%s]: %s\n", j+1, formatLine(excerptStartLineAt(r.StartLines, j)), ex)
			default:
				fmt.Printf("EXCERPT %d: %s\n", j+1, ex)
			}
		}
		fmt.Println("---")
	}
	return 0
}

// runPathOnly executes the search without the TUI and writes distinct matching
// file paths (one per line, ranked order) to stdout. Zero matches produces
// empty stdout with exit code 0 (matching grep -l / rg --files-with-matches).
// Errors go to stderr with no ANSI color.
func runPathOnly(args *Arguments) int {
	se := newSilentSearchEngine(args)
	se.PathOnly = true

	results, err := se.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		return 1
	}

	seen := make(map[string]struct{}, len(results))
	for _, r := range results {
		if r.FilePath == "" {
			continue
		}
		if _, exists := seen[r.FilePath]; exists {
			continue
		}
		seen[r.FilePath] = struct{}{}
		fmt.Println(r.FilePath)
	}
	return 0
}

func validateSearchRoot(startDir string) error {
	root := startDir
	if root == "" {
		root = "."
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("invalid search root %q: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("invalid search root %q: not a directory", root)
	}
	if _, err := os.ReadDir(root); err != nil {
		return fmt.Errorf("invalid search root %q: read directory: %w", root, err)
	}
	return nil
}

// Run parses CLI arguments and starts the TUI. Returns a process exit code.
func Run() int {
	// Parse args
	args := parseArguments(os.Args[1:])
	if len(args.SearchWords) == 0 {
		showUsage()
		return 1
	}
	// Exit early on flag validation errors
	if args.StartDirErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+args.StartDirErr.Error()))
		return 1
	}
	if args.MaxResultsErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+args.MaxResultsErr.Error()))
		return 1
	}
	if args.MaxFileSizeErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+args.MaxFileSizeErr.Error()))
		return 1
	}
	if args.PathScopeErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+args.PathScopeErr.Error()))
		return 1
	}
	if args.PartialErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+args.PartialErr.Error()))
		return 1
	}
	if err := validateSearchRoot(args.StartDir); err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+err.Error()))
		return 1
	}
	// Hook for matching layer: advertise smart-forms via environment (consumed by matching)
	if args.SmartForms {
		_ = os.Setenv("GARP_SMART_FORMS", "1")
	}

	// Path-only output mode: emit distinct matching file paths (one per line) and exit.
	// Checked before --json and --plain so --pathonly always returns only paths.
	if args.PathOnly {
		return runPathOnly(args)
	}

	// JSON output mode: preferred for agent/MCP callers. Checked before --plain so that
	// passing both flags results in JSON (the stricter / more capable format wins).
	if args.JSONOutput {
		return runJSON(args)
	}

	// Plain output mode: run search synchronously and write line-oriented text to stdout.
	// Bypasses the TUI entirely -- intended for scripts and MCP tool callers.
	if args.PlainOutput {
		return runPlain(args)
	}

	// Preflight: automatic safe mode for single-word scans over huge file counts
	// Reduce internal parallelism to protect system memory/page cache without requiring flags.
	if len(args.SearchWords) == 1 {
		if total, err := preflightFileCount(args); err == nil {
			// Threshold tuned for very large trees to avoid cache blowouts on single-term scans
			const hugeSingleWordThreshold = 200000
			if total >= hugeSingleWordThreshold {
				fmt.Println(warningStyle.Render(
					fmt.Sprintf("Large single-word scan over %d files -- enabling safe mode (reduced parallelism).", total),
				))
				// Clamp workers/concurrency to conservative values that keep memory stable.
				if args.FilterWorkers > 2 {
					args.FilterWorkers = 2
				}
				if args.HeavyConcurrency > 1 {
					args.HeavyConcurrency = 1
				}
			}
		}
	}

	// Seed model for TUI
	m := model{
		results:           []search.SearchResult{},
		currentPage:       0,
		pageSize:          1,
		totalPages:        0,
		searchTime:        0,
		quitting:          false,
		loading:           true,
		width:             0,
		height:            0,
		searchWords:       args.SearchWords,
		excludeWords:      args.ExcludeWords,
		includeCode:       args.IncludeCode,
		strict:            args.Strict,
		partial:           args.Partial,
		onlyType:          args.OnlyType,
		distance:          args.Distance,
		heavyConcurrency:  args.HeavyConcurrency,
		fileTimeoutBinary: args.FileTimeoutBinary,
		filterWorkers:     args.FilterWorkers,
		maxResults:        resolveMaxResults(args),
		maxExcerpts:       args.MaxExcerpts,
		startDir:          args.StartDir,
		pathScope:         args.PathScope,
		noIgnore:          args.NoIgnore,
		maxFileSize:       args.MaxFileSize,
		confirmSelected:   "yes",
		memUsageText:      "",
		progressText:      "",
	}

	// Start TUI
	startWall = time.Now()
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("Error:", err)
		return 1
	}
	return 0
}
