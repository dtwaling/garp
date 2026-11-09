package app

// TODO: Add support for --only <type> (e.g., --only pdf)
// - CLI parsing: parse and store an "onlyType" (single extension) mutually exclusive with --code and extension excludes.
// - Discovery: restrict file type globs to only that extension when set.
// - UI: reflect the constraint in the "Target" header line to stay truthful.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"garp/config"
	"garp/internal/terminaltext"
	"garp/search"
)

var version = "0.9.2"

type infoRequest uint8

const (
	infoNone infoRequest = iota
	infoHelp
	infoVersion
)

// Arguments for CLI flags (used to seed TUI)
type Arguments struct {
	SearchWords        []string
	ExcludeWords       []string
	ParseErr           error
	infoRequest        infoRequest
	IncludeCode        bool
	Strict             bool
	SmartForms         bool
	CaseSensitive      bool
	Distance           int
	HeavyConcurrency   int
	FilterWorkers      int
	FileTimeoutBinary  int
	MaxExcerpts        int
	MaxResults         int
	MaxResultsProvided bool
	MaxResultsErr      error // non-nil if --max-results validation failed
	OnlyType           string
	OnlyTypes          []string
	Partial            search.PartialMode
	PartialErr         error // non-nil if --partial validation failed

	// StartDir: base directory for file walks (--startdir flag).
	// Empty string means use the current working directory.
	StartDir         string
	StartDirs        []string
	StartRoots       []search.StartRoot
	StartDirProvided bool
	StartDirErr      error // non-nil if validation failed

	// PathScope: list of simple glob patterns to restrict file walks (--pathscope flag).
	// Empty slice means no restriction.
	PathScope    []string
	PathScopeErr error // non-nil if validation failed
	UserGlobs    []search.UserGlob

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

func splitFlagValue(token string) (name, value string, attached bool) {
	if index := strings.IndexByte(token, '='); index >= 0 {
		return token[:index], token[index+1:], true
	}
	return token, "", false
}

func requiredFlagValue(args []string, index int, flag, attachedValue string, attached, suggestEquals, numeric bool) (string, int, error) {
	if attached {
		if attachedValue == "" {
			return "", index, fmt.Errorf("%s: missing value", flag)
		}
		return attachedValue, index, nil
	}
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("%s: missing value", flag)
	}
	value := args[index+1]
	if value == "" {
		return "", index + 1, fmt.Errorf("%s: missing value", flag)
	}
	if strings.HasPrefix(value, "-") {
		if numeric && isNegativeInteger(value) {
			return value, index + 1, nil
		}
		if suggestEquals {
			return "", index, fmt.Errorf("%s: missing value before %q; use %s=%s for a dash-leading value", flag, value, flag, value)
		}
		return "", index, fmt.Errorf("%s: missing value before %q", flag, value)
	}
	return value, index + 1, nil
}

func isNegativeInteger(value string) bool {
	if len(value) < 2 || value[0] != '-' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func isStringValueFlag(flag string) bool {
	switch flag {
	case "--only", "-t", "--type", "--startdir", "--pathscope", "-g", "--glob", "--iglob":
		return true
	}
	return false
}

func isNumericValueFlag(flag string) bool {
	switch flag {
	case "--distance", "-distance", "--heavy-concurrency", "--file-timeout-binary",
		"--workers", "-workers", "--max-excerpts", "-m", "--max-count",
		"--max-results", "--max-filesize":
		return true
	}
	return false
}

func isContextFlag(token string) bool {
	name, _, attached := splitFlagValue(token)
	switch name {
	case "-C", "--context", "-A", "--after-context", "-B", "--before-context":
		return true
	}
	if attached || len(token) < 3 {
		return false
	}
	switch token[:2] {
	case "-C", "-A", "-B":
		for _, char := range token[2:] {
			if char < '0' || char > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func positiveInt(flag, value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q", flag, value)
	}
	if number < 1 {
		return 0, fmt.Errorf("%s: value must be positive", flag)
	}
	return number, nil
}

func boundedPositiveInt(flag, value string, maximum int) (int, error) {
	number, err := positiveInt(flag, value)
	if err != nil {
		return 0, err
	}
	if number > maximum {
		return 0, fmt.Errorf("%s: value must be at most %d", flag, maximum)
	}
	return number, nil
}

var onlyTypePattern = regexp.MustCompile(`^[a-z0-9_+-]+$`)

func appendOnlyTypes(current []string, flag, value string) ([]string, error) {
	seen := make(map[string]struct{}, len(current))
	for _, onlyType := range current {
		seen[onlyType] = struct{}{}
	}
	for _, segment := range strings.Split(value, ",") {
		normalized := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(segment), "."))
		if normalized == "" || !onlyTypePattern.MatchString(normalized) {
			return current, fmt.Errorf("%s: invalid file type %q", flag, segment)
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		current = append(current, normalized)
	}
	return current, nil
}

func repeatedRootPolicyFlagError(flag string) error {
	switch flag {
	case "--startdir":
		return fmt.Errorf("%s may only be specified once; use comma-separated paths in one %s value", flag, flag)
	case "--pathscope":
		return fmt.Errorf("%s may only be specified once; use comma-separated patterns in one %s value or repeat -g for additional path globs", flag, flag)
	default:
		return nil
	}
}

// parseArguments parses command line args.
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
	optionsEnded := false
	heavyProvided := false
	startDirSeen := false
	pathScopeSeen := false

	setParseError := func(err error) {
		if err != nil && result.ParseErr == nil {
			result.ParseErr = err
		}
	}

	for index := 0; index < len(args); index++ {
		token := args[index]
		if !optionsEnded && token == "--" {
			optionsEnded = true
			continue
		}
		if optionsEnded {
			if parsingExcludes {
				result.ExcludeWords = append(result.ExcludeWords, token)
			} else {
				result.SearchWords = append(result.SearchWords, token)
			}
			continue
		}
		if isContextFlag(token) {
			setParseError(fmt.Errorf("%s is not supported; use --distance and --max-excerpts", token))
			continue
		}
		if !strings.HasPrefix(token, "-") {
			if parsingExcludes {
				result.ExcludeWords = append(result.ExcludeWords, token)
			} else {
				result.SearchWords = append(result.SearchWords, token)
			}
			continue
		}

		flag, attachedValue, attached := splitFlagValue(token)
		valueFlag := false
		switch flag {
		case "--distance", "-distance", "--heavy-concurrency", "--file-timeout-binary",
			"--workers", "-workers", "--max-excerpts", "-m", "--max-count", "--max-results",
			"--only", "-t", "--type",
			"--startdir", "--pathscope", "-g", "--glob", "--iglob", "--max-filesize":
			valueFlag = true
		}
		if valueFlag {
			repeatedRootPolicyFlag := false
			switch flag {
			case "--startdir":
				repeatedRootPolicyFlag = startDirSeen
				startDirSeen = true
			case "--pathscope":
				repeatedRootPolicyFlag = pathScopeSeen
				pathScopeSeen = true
			}
			value, nextIndex, err := requiredFlagValue(
				args,
				index,
				flag,
				attachedValue,
				attached,
				isStringValueFlag(flag),
				isNumericValueFlag(flag),
			)
			index = nextIndex
			if repeatedRootPolicyFlag {
				setParseError(repeatedRootPolicyFlagError(flag))
				continue
			}
			if err != nil {
				switch flag {
				case "--max-results":
					result.MaxResultsProvided = true
					result.MaxResultsErr = err
				case "--max-filesize":
					result.MaxFileSize = 0
					result.MaxFileSizeErr = err
				case "--startdir":
					result.StartDirErr = err
				case "--pathscope":
					result.PathScopeErr = err
				}
				setParseError(err)
				continue
			}

			switch flag {
			case "--distance", "-distance":
				number, parseErr := positiveInt(flag, value)
				if parseErr == nil {
					result.Distance = number
				}
				setParseError(parseErr)
			case "--heavy-concurrency":
				number, parseErr := boundedPositiveInt(flag, value, search.MaxHeavyConcurrency)
				if parseErr == nil {
					result.HeavyConcurrency = number
					heavyProvided = true
				}
				setParseError(parseErr)
			case "--file-timeout-binary":
				number, parseErr := boundedPositiveInt(flag, value, search.MaxFileTimeoutBinary)
				if parseErr == nil {
					result.FileTimeoutBinary = number
				}
				setParseError(parseErr)
			case "--workers", "-workers":
				number, parseErr := boundedPositiveInt(flag, value, search.MaxWorkers)
				if parseErr == nil {
					result.FilterWorkers = number
				}
				setParseError(parseErr)
			case "--max-excerpts", "-m", "--max-count":
				number, parseErr := positiveInt(flag, value)
				if number > 50 {
					number = 50
				}
				if parseErr == nil {
					result.MaxExcerpts = number
				}
				setParseError(parseErr)
			case "--max-results":
				result.MaxResultsProvided = true
				result.MaxResultsErr = nil
				number, parseErr := strconv.Atoi(value)
				switch {
				case parseErr != nil:
					result.MaxResultsErr = fmt.Errorf("--max-results: invalid integer %q", value)
				case number < 0:
					result.MaxResultsErr = fmt.Errorf("--max-results: value must be non-negative")
				default:
					result.MaxResults = number
				}
				setParseError(result.MaxResultsErr)
			case "--only", "-t", "--type":
				var parseErr error
				result.OnlyTypes, parseErr = appendOnlyTypes(result.OnlyTypes, flag, value)
				if len(result.OnlyTypes) > 0 {
					result.OnlyType = result.OnlyTypes[0]
				}
				setParseError(parseErr)
			case "--startdir":
				result.StartDirErr = nil
				cleaned, parseErr := search.ValidateStartDir(value)
				if parseErr != nil {
					result.StartDirErr = parseErr
				} else {
					result.StartDirs = append(result.StartDirs, value)
					if !result.StartDirProvided {
						result.StartDir = firstStartDirCompatibility(cleaned)
					}
					result.StartDirProvided = true
				}
				setParseError(parseErr)
			case "--pathscope":
				result.PathScopeErr = nil
				segments, parseErr := search.ValidatePathScope(value)
				if parseErr != nil {
					result.PathScopeErr = parseErr
				} else {
					result.PathScope = segments
				}
				setParseError(parseErr)
			case "-g", "--glob", "--iglob":
				glob, parseErr := search.ValidateUserGlob(value, flag == "--iglob")
				if parseErr == nil {
					result.UserGlobs = append(result.UserGlobs, glob)
				} else {
					parseErr = fmt.Errorf("%s: %w", flag, parseErr)
				}
				setParseError(parseErr)
			case "--max-filesize":
				result.MaxFileSize, result.MaxFileSizeErr = search.ParseMaxFileSize(value)
				setParseError(result.MaxFileSizeErr)
			}
			continue
		}

		if attached {
			switch flag {
			case "--code", "--strict", "--not", "--no-ignore", "--smart-forms",
				"--plain", "--json", "--pathonly", "-l", "--files-with-matches",
				"-i", "--ignore-case", "-s", "--case-sensitive",
				"--help", "-h", "--version", "-v":
				setParseError(fmt.Errorf("%s does not accept a value", flag))
			case "--partial":
				switch attachedValue {
				case "off", "none":
					result.Partial = search.PartialModeOff
				case string(search.PartialModePrefix):
					result.Partial = search.PartialModePrefix
				case string(search.PartialModeContains):
					result.Partial = search.PartialModeContains
				default:
					result.PartialErr = fmt.Errorf("invalid --partial mode %q; supported modes: prefix, contains, off, none", attachedValue)
					setParseError(result.PartialErr)
				}
			default:
				setParseError(fmt.Errorf("unknown flag %q", flag))
			}
			continue
		}

		switch flag {
		case "--code":
			result.IncludeCode = true
		case "--strict":
			result.Strict = true
		case "--not":
			parsingExcludes = true
		case "--no-ignore":
			result.NoIgnore = true
		case "--smart-forms":
			result.SmartForms = true
		case "-i", "--ignore-case":
			result.CaseSensitive = false
		case "-s", "--case-sensitive":
			result.CaseSensitive = true
		case "--partial":
			result.Partial = search.PartialModePrefix
		case "--plain":
			result.PlainOutput = true
		case "--json":
			result.JSONOutput = true
		case "--pathonly", "-l", "--files-with-matches":
			result.PathOnly = true
		case "--help", "-h":
			if result.infoRequest == infoNone {
				result.infoRequest = infoHelp
			}
		case "--version", "-v":
			if result.infoRequest == infoNone {
				result.infoRequest = infoVersion
			}
		default:
			switch {
			case strings.HasPrefix(token, "-"):
				setParseError(fmt.Errorf("unknown flag %q", token))
			case parsingExcludes:
				result.ExcludeWords = append(result.ExcludeWords, token)
			default:
				result.SearchWords = append(result.SearchWords, token)
			}
		}
	}
	result.SearchWords = search.NormalizeQueryTermsWithOptions(result.SearchWords, search.QueryOptions{
		CaseSensitive: result.CaseSensitive,
		SmartForms:    result.SmartForms,
	})

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
		if derived > search.MaxHeavyConcurrency {
			derived = search.MaxHeavyConcurrency
		}
		result.HeavyConcurrency = derived
	}
	return result
}

func firstStartDirCompatibility(value string) string {
	first := strings.TrimSpace(strings.SplitN(value, ",", 2)[0])
	if len(first) >= 2 && ((first[0] == '"' && first[len(first)-1] == '"') ||
		(first[0] == '\'' && first[len(first)-1] == '\'')) {
		first = strings.TrimSpace(first[1 : len(first)-1])
	}
	return filepath.Clean(first)
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

func newTUIModel(args *Arguments) model {
	return model{
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
		caseSensitive:     args.CaseSensitive,
		smartForms:        args.SmartForms,
		partial:           args.Partial,
		onlyType:          args.OnlyType,
		onlyTypes:         args.OnlyTypes,
		distance:          args.Distance,
		heavyConcurrency:  args.HeavyConcurrency,
		fileTimeoutBinary: args.FileTimeoutBinary,
		filterWorkers:     args.FilterWorkers,
		maxResults:        resolveMaxResults(args),
		maxExcerpts:       args.MaxExcerpts,
		startDir:          args.StartDir,
		startRoots:        args.StartRoots,
		pathScope:         args.PathScope,
		userGlobs:         args.UserGlobs,
		noIgnore:          args.NoIgnore,
		maxFileSize:       args.MaxFileSize,
		confirmSelected:   "yes",
		memUsageText:      "",
		progressText:      "",
	}
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
	fmt.Println(infoStyle.Render(wrapTextWithIndent("  garp ", "[--code] [--strict] [--partial[=MODE]] [--distance N] [--max-excerpts N] [--max-results N]\n[--startdir PATH] [--pathscope PATTERNS] [--no-ignore] [--max-filesize SIZE]\n[-g PATTERN] [-t TYPE] [-i|-s] [-l] [--plain] [--json] <word1> <word2> ...\n[--not <exclude1> <exclude2> ...] [-- <literal> ...]", 100)))
	fmt.Println()

	// Flags
	fmt.Println(subHeaderStyle.Render("FLAGS"))
	fmt.Println(infoStyle.Render("  --code                  Include code files in the search"))
	fmt.Println(infoStyle.Render("  --strict                Require all search terms to match"))
	fmt.Println(infoStyle.Render("  -i, --ignore-case       Match terms without case sensitivity (default)"))
	fmt.Println(infoStyle.Render("  -s, --case-sensitive    Match terms with case sensitivity; last case flag wins"))
	fmt.Println(infoStyle.Render("  --partial[=MODE]        Enable partial word matching (prefix, contains)."))
	fmt.Println(infoStyle.Render("                          Bare form selects prefix; off and none select whole-word matching."))
	fmt.Println(infoStyle.Render("                          Tip: valid query terms support inline * and ?."))
	fmt.Println(infoStyle.Render("                          Invalid or over-limit wildcard terms match literally. See README for bounds."))
	fmt.Println(infoStyle.Render("  --distance N            Proximity window in characters (default 5000)"))
	fmt.Println(infoStyle.Render("  -m, --max-count N       Alias for --max-excerpts, not an rg line-count limit"))
	fmt.Println(infoStyle.Render("  --max-excerpts N        Maximum excerpts per file; >=90% new content (up to 10% overlap; default 1, max 50)"))
	fmt.Println(infoStyle.Render("  --max-results N         Maximum matching files; 0 = unlimited."))
	fmt.Println(infoStyle.Render("                          Defaults: --json 50, --plain 50, --pathonly 0, TUI 0."))
	fmt.Println(infoStyle.Render(fmt.Sprintf("  --heavy-concurrency N   Concurrent heavy extractions (auto if omitted, max %d)", search.MaxHeavyConcurrency)))
	fmt.Println(infoStyle.Render(fmt.Sprintf("  --workers N             Stage 2 text filter workers (default 4); max %d", search.MaxWorkers)))
	fmt.Println(infoStyle.Render(fmt.Sprintf("  --file-timeout-binary N Timeout in ms for binary extraction (default 1000, max %d)", search.MaxFileTimeoutBinary)))
	fmt.Println(infoStyle.Render("  --smart-forms           Enable smart word forms (s, es, ed, ing, al, tion/ation)"))
	fmt.Println(infoStyle.Render("  -t, --type, --only TYPE Restrict file types; repeat or use comma-separated values"))
	fmt.Println(infoStyle.Render("  --startdir PATH         Comma-separated search roots; specify this flag once"))
	fmt.Println(infoStyle.Render("                          Existing directory names that contain commas stay intact"))
	fmt.Println(infoStyle.Render("  --pathscope PATTERNS   Comma-separated positive scopes form a union; specify this flag once"))
	fmt.Println(infoStyle.Render("                         Use repeatable -g for additional path globs"))
	fmt.Println(infoStyle.Render("                         Bare name matches basenames at any depth"))
	fmt.Println(infoStyle.Render("                         name/, ./name, and patterns containing / are root-anchored"))
	fmt.Println(infoStyle.Render("                         * and ? stay within one segment; ** crosses segments"))
	fmt.Println(infoStyle.Render("                         Use ./src for explicit root-relative matching"))
	fmt.Println(infoStyle.Render("                         Absolute pathscope and user-glob patterns must overlap a search root"))
	fmt.Println(infoStyle.Render("                         Examples: POSIX /repo/src/**; Windows C:/repo/src/**"))
	fmt.Println(infoStyle.Render("                         A pattern can select inside a root or cover it as an ancestor"))
	fmt.Println(infoStyle.Render("                         On Windows, a non-absolute leading slash is root-relative"))
	fmt.Println(infoStyle.Render("                         If no root intersects it, validation fails before search"))
	fmt.Println(infoStyle.Render("                         Prefix with ! to exclude a path"))
	fmt.Println(infoStyle.Render("                         Pathscope intersects with positive --glob and --iglob patterns"))
	fmt.Println(infoStyle.Render("  -g, --glob PATTERN     Add a case-sensitive path glob; repeat for a union"))
	fmt.Println(infoStyle.Render("  --iglob PATTERN        Add a case-insensitive path glob"))
	fmt.Println(infoStyle.Render("                         Prefix with ! to exclude; ** crosses directory boundaries"))
	fmt.Println(infoStyle.Render("  --no-ignore            Do not apply repository ignore files (.gitignore, .ignore, or exclude)."))
	fmt.Println(infoStyle.Render("  --max-filesize SIZE    Maximum file size (B, K/KB/KiB, M/MB/MiB; maximum 100M)."))
	fmt.Println(infoStyle.Render("  -l, --files-with-matches"))
	fmt.Println(infoStyle.Render("  --pathonly             Emit only distinct matching file paths, one per line"))
	fmt.Println(infoStyle.Render("  --plain                Skip the interactive TUI; emit plain line-oriented output to stdout."))
	fmt.Println(infoStyle.Render("                         Useful for scripting and MCP tool callers that need machine-readable results."))
	fmt.Println(infoStyle.Render("  --json                 Skip the interactive TUI; emit a JSON document to stdout."))
	fmt.Println(infoStyle.Render("                         Preferred over --plain for agent/MCP callers -- unambiguous, no parse fragility."))
	fmt.Println(infoStyle.Render("  --not ...               Tokens after this are exclusions;"))
	fmt.Println(infoStyle.Render("                          extensions starting with '.' exclude types; others exclude words"))
	fmt.Println(infoStyle.Render("  --                      Stop flag parsing; remaining tokens are literal terms or exclusions"))
	fmt.Println(infoStyle.Render("  -C/-A/-B context flags are not supported; use --distance and --max-excerpts"))
	fmt.Println(infoStyle.Render("  Value flags accept both --flag VALUE and --flag=VALUE forms"))
	fmt.Println(infoStyle.Render("  Listed aliases are compatible with rg; garp is not a drop-in rg replacement"))
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
	Query     jsonQuery    `json:"query"`
	Matches   int          `json:"matches"`
	Returned  int          `json:"returned"`
	Truncated bool         `json:"truncated"`
	Results   []jsonResult `json:"results"`
}

// jsonQuery echoes the search parameters back to the caller so the response is self-describing.
type jsonQuery struct {
	Terms         []string `json:"terms"`
	Strict        bool     `json:"strict,omitempty"`
	Excludes      []string `json:"excludes,omitempty"`
	Partial       string   `json:"partial,omitempty"`
	StartDir      string   `json:"start_dir,omitempty"`
	StartDirs     []string `json:"start_dirs,omitempty"`
	PathScope     []string `json:"path_scope,omitempty"`
	OnlyType      string   `json:"only_type,omitempty"`
	OnlyTypes     []string `json:"only_types,omitempty"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`
	Code          bool     `json:"include_code"`
	NoIgnore      bool     `json:"no_ignore,omitempty"`
	MaxFileSize   int64    `json:"max_filesize,omitempty"`
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
func buildFileTypes(includeCode bool, onlyType string, onlyTypes []string) []string {
	effective := effectiveOnlyTypes(onlyType, onlyTypes)
	if len(effective) > 0 {
		return config.OnlyTypesGlobs(effective)
	}
	return config.BuildRipgrepFileTypes(includeCode)
}

func effectiveOnlyTypes(onlyType string, onlyTypes []string) []string {
	if len(onlyTypes) > 0 {
		return onlyTypes
	}
	if onlyType != "" {
		return []string{onlyType}
	}
	return nil
}

func positiveGlobCodeEligibility(onlyType string, onlyTypes []string) bool {
	return len(effectiveOnlyTypes(onlyType, onlyTypes)) == 0
}

func walkOptions(args *Arguments) search.WalkOptions {
	return search.WalkOptions{
		NoIgnore:                    args.NoIgnore,
		MaxFileSize:                 args.MaxFileSize,
		UserGlobs:                   args.UserGlobs,
		PositiveGlobCodeEligibility: positiveGlobCodeEligibility(args.OnlyType, args.OnlyTypes),
	}
}

func preflightFileCount(args *Arguments) (int, error) {
	fileTypes := buildFileTypes(args.IncludeCode, args.OnlyType, args.OnlyTypes)
	roots := args.StartRoots
	if len(roots) == 0 {
		raw := args.StartDirs
		if len(raw) == 0 && args.StartDir != "" {
			raw = []string{args.StartDir}
		}
		var err error
		roots, err = search.PrepareStartRootsWithOptions(raw, search.StartRootOptions{
			NoIgnore:  args.NoIgnore,
			PathScope: args.PathScope,
			UserGlobs: args.UserGlobs,
		})
		if err != nil {
			return 0, err
		}
	}
	return search.GetDocumentFileCountForRoots(fileTypes, roots, args.PathScope, walkOptions(args))
}

// newSilentSearchEngine configures a silent SearchEngine for non-TUI CLI modes
// (--json, --plain, --pathonly).
func newSilentSearchEngine(args *Arguments) *search.SearchEngine {
	fileTypes := buildFileTypes(args.IncludeCode, args.OnlyType, args.OnlyTypes)
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
	se.CaseSensitive = args.CaseSensitive
	se.SmartForms = args.SmartForms
	se.PathOnly = args.PathOnly
	se.NoIgnore = args.NoIgnore
	se.MaxFileSize = args.MaxFileSize
	if args.Distance > 0 {
		se.Distance = args.Distance
	}
	if args.StartDir != "" {
		se.StartDir = args.StartDir
	}
	se.StartRoots = args.StartRoots
	if len(args.PathScope) > 0 {
		se.PathScope = args.PathScope
	}
	se.UserGlobs = args.UserGlobs
	se.PositiveGlobCodeEligibility = positiveGlobCodeEligibility(args.OnlyType, args.OnlyTypes)
	se.MaxExcerpts = args.MaxExcerpts
	se.MaxResults = resolveMaxResults(args)
	return se
}

// runJSON executes the search without the TUI and writes a JSON document to stdout.
// Errors go to stderr as plain text; the exit code is non-zero on failure.
func runJSON(args *Arguments) int {
	se := newSilentSearchEngine(args)

	outcome, err := se.ExecuteDetailed()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+terminaltext.Sanitize(err.Error()))
		return 1
	}

	out := buildJSONOutput(args, outcome)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	// Don't HTML-escape <, >, & -- code excerpts (PHP "<?php", Delphi generics, "&&") are
	// far more readable as raw characters, and stdout consumers don't need HTML safety.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "Error encoding JSON: "+terminaltext.Sanitize(err.Error()))
		return 1
	}
	return 0
}

func buildJSONOutput(args *Arguments, outcome search.ExecutionResult) jsonOutput {
	out := jsonOutput{
		Query: jsonQuery{
			Terms:         args.SearchWords,
			Strict:        args.Strict,
			Excludes:      args.ExcludeWords,
			Partial:       string(args.Partial),
			StartDir:      args.StartDir,
			PathScope:     nativePathScope(args.PathScope),
			OnlyType:      args.OnlyType,
			CaseSensitive: args.CaseSensitive,
			Code:          args.IncludeCode,
			NoIgnore:      args.NoIgnore,
			MaxFileSize:   args.MaxFileSize,
		},
		Matches:   outcome.Matches,
		Returned:  outcome.Returned,
		Truncated: outcome.Truncated,
		Results:   make([]jsonResult, 0, outcome.Returned),
	}
	if len(args.OnlyTypes) >= 2 {
		out.Query.OnlyTypes = append([]string(nil), args.OnlyTypes...)
	}
	if !args.StartDirProvided {
		out.Query.StartDir = ""
	} else if len(args.StartRoots) > 0 {
		reported := search.ReportedStartRoots(args.StartRoots)
		if len(reported) > 0 {
			out.Query.StartDir = reported[0].SuppliedPath
		}
		if len(reported) >= 2 {
			out.Query.StartDirs = make([]string, len(reported))
			for index := range reported {
				out.Query.StartDirs[index] = reported[index].SuppliedPath
			}
		}
	}

	for _, r := range outcome.Results {
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
	return out
}

// runPlain executes the search without the TUI and writes clean text to stdout.
// Output format:
//
//	MATCH <n>/<returned>
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

	outcome, err := se.ExecuteDetailed()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+terminaltext.Sanitize(err.Error()))
		return 1
	}
	writePlainOutput(os.Stdout, args, outcome)
	return 0
}

// escapeOutputLine uses Go string escaping without surrounding quotes so data
// cannot introduce output lines or terminal controls. Backslashes are escaped
// too, keeping literal escape sequences distinct from encoded controls.
func escapeOutputLine(s string) string {
	quoted := strconv.Quote(s)
	return quoted[1 : len(quoted)-1]
}

func writePlainOutput(w io.Writer, args *Arguments, outcome search.ExecutionResult) {
	if args.Partial != search.PartialModeOff {
		fmt.Fprintf(w, "Mode: partial (%s)\n", args.Partial)
	}

	if outcome.Returned == 0 {
		fmt.Fprintln(w, "NO RESULTS")
		return
	}

	// Use RawExcerpts (pre-highlight), escaping untrusted data per line.
	// Excerpt headers carry per-chunk ranked quality when the chunk framed a
	// ranked window; the old "[L<start>]" form is kept for quality-less chunks.
	for i, r := range outcome.Results {
		fmt.Fprintf(w, "MATCH %d/%d [Score: %d (%d/%d terms)]\n", i+1, outcome.Returned, r.Score, r.TermCount, len(args.SearchWords))
		fmt.Fprintf(w, "FILE: %s\n", escapeOutputLine(r.FilePath))
		fmt.Fprintf(w, "SIZE: %d\n", r.FileSize)
		for j, ex := range r.RawExcerpts {
			ex = escapeOutputLine(ex)
			score := excerptChunkScoreAt(r.ChunkScores, j)
			terms := excerptChunkTermCountAt(r.ChunkTermCounts, j)
			switch {
			case score > 0 && terms > 0 && formatLine(excerptStartLineAt(r.StartLines, j)) != "":
				fmt.Fprintf(w, "EXCERPT %d [%s, Score: %d (%d terms)]: %s\n", j+1, formatLine(excerptStartLineAt(r.StartLines, j)), score, terms, ex)
			case score > 0 && terms > 0:
				fmt.Fprintf(w, "EXCERPT %d [Score: %d (%d terms)]: %s\n", j+1, score, terms, ex)
			case formatLine(excerptStartLineAt(r.StartLines, j)) != "":
				fmt.Fprintf(w, "EXCERPT %d [%s]: %s\n", j+1, formatLine(excerptStartLineAt(r.StartLines, j)), ex)
			default:
				fmt.Fprintf(w, "EXCERPT %d: %s\n", j+1, ex)
			}
		}
		fmt.Fprintln(w, "---")
	}
	if outcome.Truncated {
		fmt.Fprintf(w, "# truncated: showing %d of %d matching files (use --max-results 0 for all)\n", outcome.Returned, outcome.Matches)
	}
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
		fmt.Fprintln(os.Stderr, "Error: "+terminaltext.Sanitize(err.Error()))
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
		fmt.Println(escapeOutputLine(r.FilePath))
	}
	return 0
}

func prepareArgumentStartRoots(args *Arguments) error {
	rawRoots := args.StartDirs
	if !args.StartDirProvided {
		rawRoots = nil
	}
	roots, err := search.PrepareStartRootsWithOptions(rawRoots, search.StartRootOptions{
		NoIgnore:  args.NoIgnore,
		PathScope: args.PathScope,
		UserGlobs: args.UserGlobs,
	})
	if err != nil {
		return err
	}
	args.StartRoots = roots
	if args.StartDirProvided {
		reportedRoots := search.ReportedStartRoots(roots)
		if len(reportedRoots) > 0 {
			args.StartDir = reportedRoots[0].SuppliedPath
		}
	}
	return nil
}

// Run parses CLI arguments and starts the TUI. Returns a process exit code.
func Run() int {
	// Parse args
	args := parseArguments(os.Args[1:])
	if args.ParseErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+terminaltext.Sanitize(args.ParseErr.Error())))
		return 1
	}
	switch args.infoRequest {
	case infoHelp:
		showUsage()
		return 0
	case infoVersion:
		showVersion()
		return 0
	}
	if len(args.SearchWords) == 0 {
		showUsage()
		return 1
	}
	// Exit early on flag validation errors
	if args.StartDirErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+terminaltext.Sanitize(args.StartDirErr.Error())))
		return 1
	}
	if args.MaxResultsErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+terminaltext.Sanitize(args.MaxResultsErr.Error())))
		return 1
	}
	if args.MaxFileSizeErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+terminaltext.Sanitize(args.MaxFileSizeErr.Error())))
		return 1
	}
	if args.PathScopeErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+terminaltext.Sanitize(args.PathScopeErr.Error())))
		return 1
	}
	if args.PartialErr != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+terminaltext.Sanitize(args.PartialErr.Error())))
		return 1
	}
	if err := prepareArgumentStartRoots(args); err != nil {
		fmt.Fprintln(os.Stderr, errorStyle.Render("Error: "+terminaltext.Sanitize(err.Error())))
		return 1
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

	m := newTUIModel(args)

	// Start TUI
	startWall = time.Now()
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("Error:", terminaltext.Sanitize(err.Error()))
		return 1
	}
	return 0
}
