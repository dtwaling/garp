package app

// TODO: Add support for --only <type> (e.g., --only pdf)
// - CLI parsing: parse and store an "onlyType" (single extension) mutually exclusive with --code and extension excludes.
// - Discovery: restrict file type globs to only that extension when set.
// - UI: reflect the constraint in the "Target" header line to stay truthful.

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"garp/config"
	"garp/search"
)

var version = "0.7"

// Arguments for CLI flags (used to seed TUI)
type Arguments struct {
	SearchWords       []string
	ExcludeWords      []string
	IncludeCode       bool
	SmartForms        bool
	Distance          int
	HeavyConcurrency  int
	FilterWorkers     int
	FileTimeoutBinary int
	OnlyType          string

	// StartDir: base directory for file walks (--startdir flag).
	// Empty string means use the current working directory.
	StartDir    string
	StartDirErr error // non-nil if validation failed

	// PathScope: list of simple glob patterns to restrict file walks (--pathscope flag).
	// Empty slice means no restriction.
	PathScope    []string
	PathScopeErr error // non-nil if validation failed

	// PlainOutput: when true, skip the TUI and emit clean line-oriented text to stdout.
	// Designed for programmatic callers (MCP tools, scripts) that need machine-readable output.
	PlainOutput bool

	// JSONOutput: when true, skip the TUI and emit a JSON document to stdout.
	// Preferred over --plain for agent/MCP callers -- unambiguous structure, no parse fragility.
	JSONOutput bool
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
	}

	parsingExcludes := false
	expectDistance := false
	expectHeavy := false
	expectTimeout := false
	expectWorkers := false
	expectOnly := false
	expectStartDir := false
	expectPathScope := false
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
		switch a {
		case "--code":
			result.IncludeCode = true
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
		case "--only":
			expectOnly = true
		case "--startdir":
			expectStartDir = true
		case "--pathscope":
			expectPathScope = true
		case "--smart-forms":
			result.SmartForms = true
		case "--plain":
			result.PlainOutput = true
		case "--json":
			result.JSONOutput = true
		case "--help", "-h":
			showUsage()
			os.Exit(0)
		case "--version", "-v":
			showVersion()
			os.Exit(0)
		default:
			if parsingExcludes {
				result.ExcludeWords = append(result.ExcludeWords, a)
			} else {
				result.SearchWords = append(result.SearchWords, a)
			}
		}
	}

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
	fmt.Println(infoStyle.Render(wrapTextWithIndent("  garp ", "[--code] [--distance N] [--heavy-concurrency N] [--workers N] [--file-timeout-binary N] <word1> <word2> ... [--not <exclude1> <exclude2> ...]", 100)))
	fmt.Println()

	// Flags
	fmt.Println(subHeaderStyle.Render("FLAGS"))
	fmt.Println(infoStyle.Render("  --code                  Include code files in the search"))
	fmt.Println(infoStyle.Render("  --distance N            Proximity window in characters (default 5000)"))
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
// jsonResult is a single file match in --json output.
type jsonResult struct {
	File    string   `json:"file"`
	SizeB   int64    `json:"size_bytes"`
	Excerpts []string `json:"excerpts"`
}

// jsonOutput is the top-level envelope emitted by --json.
type jsonOutput struct {
	Query   jsonQuery    `json:"query"`
	Matches int          `json:"matches"`
	Results []jsonResult `json:"results"`
}

// jsonQuery echoes the search parameters back to the caller so the response is self-describing.
type jsonQuery struct {
	Terms     []string `json:"terms"`
	Excludes  []string `json:"excludes,omitempty"`
	StartDir  string   `json:"start_dir,omitempty"`
	PathScope []string `json:"path_scope,omitempty"`
	OnlyType  string   `json:"only_type,omitempty"`
	Code      bool     `json:"include_code"`
}

// runJSON executes the search without the TUI and writes a JSON document to stdout.
// Errors go to stderr as plain text; the exit code is non-zero on failure.
func runJSON(args *Arguments) int {
	fileTypes := config.BuildRipgrepFileTypes(args.IncludeCode)
	if args.OnlyType != "" {
		fileTypes = []string{"-g", "*." + strings.TrimPrefix(strings.ToLower(args.OnlyType), ".")}
	}
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
	if args.Distance > 0 {
		se.Distance = args.Distance
	}
	if args.StartDir != "" {
		se.StartDir = args.StartDir
	}
	if len(args.PathScope) > 0 {
		se.PathScope = args.PathScope
	}

	results, err := se.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		return 1
	}

	out := jsonOutput{
		Query: jsonQuery{
			Terms:     args.SearchWords,
			Excludes:  args.ExcludeWords,
			StartDir:  args.StartDir,
			PathScope: args.PathScope,
			OnlyType:  args.OnlyType,
			Code:      args.IncludeCode,
		},
		Matches: len(results),
		Results: make([]jsonResult, 0, len(results)),
	}

	for _, r := range results {
		out.Results = append(out.Results, jsonResult{
			File:     r.FilePath,
			SizeB:    r.FileSize,
			Excerpts: r.RawExcerpts,
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
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
//	EXCERPT <i>: <text>
//	---
//
// Zero matches produces a single "NO RESULTS" line.
// Errors go to stderr with no ANSI color.
func runPlain(args *Arguments) int {
	fileTypes := config.BuildRipgrepFileTypes(args.IncludeCode)
	if args.OnlyType != "" {
		fileTypes = []string{"-g", "*." + strings.TrimPrefix(strings.ToLower(args.OnlyType), ".")}
	}
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
	if args.Distance > 0 {
		se.Distance = args.Distance
	}
	if args.StartDir != "" {
		se.StartDir = args.StartDir
	}
	if len(args.PathScope) > 0 {
		se.PathScope = args.PathScope
	}

	results, err := se.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		return 1
	}

	if len(results) == 0 {
		fmt.Println("NO RESULTS")
		return 0
	}

	// Use RawExcerpts (pre-highlight) -- no ANSI stripping needed.
	for i, r := range results {
		fmt.Printf("MATCH %d/%d\n", i+1, len(results))
		fmt.Printf("FILE: %s\n", r.FilePath)
		fmt.Printf("SIZE: %d\n", r.FileSize)
		for j, ex := range r.RawExcerpts {
			fmt.Printf("EXCERPT %d: %s\n", j+1, ex)
		}
		fmt.Println("---")
	}
	return 0
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
	if args.PathScopeErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+args.PathScopeErr.Error()))
		return 1
	}
	// Hook for matching layer: advertise smart-forms via environment (consumed by matching)
	if args.SmartForms {
		_ = os.Setenv("GARP_SMART_FORMS", "1")
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
		fileTypes := config.BuildRipgrepFileTypes(args.IncludeCode)
		if args.OnlyType != "" {
			fileTypes = []string{"-g", "*." + strings.TrimPrefix(strings.ToLower(args.OnlyType), ".")}
		}
		if total, err := search.GetDocumentFileCount(fileTypes, "", nil); err == nil {
			// Threshold tuned for very large trees to avoid cache blowouts on single-term scans
			const hugeSingleWordThreshold = 200000
			if total >= hugeSingleWordThreshold {
				fmt.Println(warningStyle.Render(
					fmt.Sprintf("Large single-word scan over %d files — enabling safe mode (reduced parallelism).", total),
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
		onlyType:          args.OnlyType,
		distance:          args.Distance,
		heavyConcurrency:  args.HeavyConcurrency,
		fileTimeoutBinary: args.FileTimeoutBinary,
		filterWorkers:     args.FilterWorkers,
		startDir:          args.StartDir,
		pathScope:         args.PathScope,
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
