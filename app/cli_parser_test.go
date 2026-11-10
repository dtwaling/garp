package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

func TestParseArgumentsMaxFileSize(t *testing.T) {
	t.Run("valid value", func(t *testing.T) {
		args := parseArguments([]string{"needle", "--max-filesize", "12MiB"})
		if args.MaxFileSizeErr != nil {
			t.Fatalf("MaxFileSizeErr = %v, want nil", args.MaxFileSizeErr)
		}
		if args.MaxFileSize != 12*1024*1024 {
			t.Errorf("MaxFileSize = %d, want %d", args.MaxFileSize, 12*1024*1024)
		}
	})

	t.Run("missing terminal value", func(t *testing.T) {
		args := parseArguments([]string{"needle", "--max-filesize"})
		if args.MaxFileSizeErr == nil {
			t.Fatal("MaxFileSizeErr = nil, want missing-value error")
		}
	})

	t.Run("following flag is not consumed", func(t *testing.T) {
		args := parseArguments([]string{"needle", "--max-filesize", "--no-ignore"})
		if args.MaxFileSizeErr == nil {
			t.Fatal("MaxFileSizeErr = nil, want invalid-value error")
		}
		if !args.NoIgnore {
			t.Error("NoIgnore = false, following flag was consumed as max-filesize value")
		}
	})

	t.Run("later valid value preserves first parse error", func(t *testing.T) {
		args := parseArguments([]string{
			"needle",
			"--max-filesize", "invalid",
			"--max-filesize", "2M",
		})
		if args.MaxFileSizeErr != nil {
			t.Fatalf("MaxFileSizeErr = %v, want nil from latest value", args.MaxFileSizeErr)
		}
		if args.MaxFileSize != 2*1024*1024 {
			t.Errorf("MaxFileSize = %d, want %d", args.MaxFileSize, 2*1024*1024)
		}
		if args.ParseErr == nil {
			t.Fatal("ParseErr = nil, want first max-filesize error")
		}
	})

	t.Run("latest invalid value clears prior size", func(t *testing.T) {
		args := parseArguments([]string{
			"needle",
			"--max-filesize", "2M",
			"--max-filesize", "invalid",
		})
		if args.MaxFileSizeErr == nil {
			t.Fatal("MaxFileSizeErr = nil, want error from latest value")
		}
		if args.MaxFileSize != 0 {
			t.Errorf("MaxFileSize = %d, want 0 after latest value failed", args.MaxFileSize)
		}
	})
}

func TestParseArgumentsNoIgnore(t *testing.T) {
	args := parseArguments([]string{"needle", "--no-ignore"})
	if !args.NoIgnore {
		t.Error("NoIgnore = false, want true")
	}
}

func TestParseArgumentsMaxResults(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		want     int
		provided bool
		wantErr  bool
		parseErr bool
	}{
		{name: "positive", argv: []string{"needle", "--max-results", "12"}, want: 12, provided: true},
		{name: "explicit zero", argv: []string{"needle", "--max-results", "0"}, provided: true},
		{name: "missing value", argv: []string{"needle", "--max-results"}, provided: true, wantErr: true, parseErr: true},
		{name: "negative value", argv: []string{"needle", "--max-results", "-1"}, provided: true, wantErr: true, parseErr: true},
		{name: "non-integer value", argv: []string{"needle", "--max-results", "many"}, provided: true, wantErr: true, parseErr: true},
		{name: "latest repeated value wins", argv: []string{"needle", "--max-results", "3", "--max-results", "7"}, want: 7, provided: true},
		{name: "later valid value preserves first parse error", argv: []string{"needle", "--max-results", "bad", "--max-results", "5"}, want: 5, provided: true, parseErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := parseArguments(tt.argv)
			if args.MaxResults != tt.want {
				t.Errorf("MaxResults = %d, want %d", args.MaxResults, tt.want)
			}
			if args.MaxResultsProvided != tt.provided {
				t.Errorf("MaxResultsProvided = %t, want %t", args.MaxResultsProvided, tt.provided)
			}
			if (args.MaxResultsErr != nil) != tt.wantErr {
				t.Fatalf("MaxResultsErr = %v, want error %t", args.MaxResultsErr, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(args.MaxResultsErr.Error(), "--max-results") {
				t.Errorf("MaxResultsErr = %q, want --max-results", args.MaxResultsErr)
			}
			if (args.ParseErr != nil) != tt.parseErr {
				t.Errorf("ParseErr = %v, want error %t", args.ParseErr, tt.parseErr)
			}
			if len(args.SearchWords) != 1 || args.SearchWords[0] != "needle" {
				t.Errorf("SearchWords = %q, max-results value was not consumed", args.SearchWords)
			}
		})
	}
}

func TestCLIArgumentsPartial(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		partial search.PartialMode
		wantErr bool
	}{
		{
			name:    "default is off",
			argv:    []string{"deploy"},
			partial: search.PartialModeOff,
		},
		{
			name:    "bare flag enables prefix",
			argv:    []string{"deploy", "--partial"},
			partial: search.PartialModePrefix,
		},
		{
			name:    "prefix mode",
			argv:    []string{"deploy", "--partial=prefix"},
			partial: search.PartialModePrefix,
		},
		{
			name:    "contains mode",
			argv:    []string{"deploy", "--partial=contains"},
			partial: search.PartialModeContains,
		},
		{
			name:    "off mode",
			argv:    []string{"deploy", "--partial=off"},
			partial: search.PartialModeOff,
		},
		{
			name:    "invalid mode reports validation error",
			argv:    []string{"deploy", "--partial=fuzzy"},
			wantErr: true,
		},
		{
			name:    "combines with other flags",
			argv:    []string{"deploy", "--partial=prefix", "--code", "--distance", "200", "--json"},
			partial: search.PartialModePrefix,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := parseArguments(tt.argv)
			if tt.wantErr {
				if args.PartialErr == nil {
					t.Fatal("PartialErr = nil, want validation error")
				}
				return
			}
			if args.PartialErr != nil {
				t.Fatalf("PartialErr = %v, want nil", args.PartialErr)
			}
			if args.Partial != tt.partial {
				t.Errorf("Partial = %q, want %q", args.Partial, tt.partial)
			}
		})
	}
}

func TestCLIArgumentsCanonicalTerms(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "service.md")

	args := parseArguments([]string{
		"ms-**-api",
		"MS-*-API",
		"--startdir", root,
		"--only", "md",
	})
	if !slicesEqual(args.SearchWords, []string{"ms-*-api"}) {
		t.Fatalf("SearchWords = %q, want [ms-*-api]", args.SearchWords)
	}

	engine := newSilentSearchEngine(args)
	if !slicesEqual(engine.SearchWords, []string{"ms-*-api"}) {
		t.Fatalf("engine SearchWords = %q, want [ms-*-api]", engine.SearchWords)
	}

	out := captureCLIStdout(t, func() int { return runJSON(args) })
	var result jsonOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if !slicesEqual(result.Query.Terms, []string{"ms-*-api"}) {
		t.Errorf("JSON query terms = %q, want [ms-*-api]", result.Query.Terms)
	}

	lastExcerptInnerWidth = 0
	lastContentHeight = 0
	view := (model{
		results: []search.SearchResult{{
			FilePath:     fixture,
			Score:        len("ms-*-api"),
			TermCount:    1,
			MatchedTerms: []string{"ms-*-api"},
			Excerpts:     []string{"ms-option-api"},
		}},
		width:           200,
		height:          60,
		searchWords:     args.SearchWords,
		totalFiles:      1,
		confirmSelected: "yes",
	}).View()
	if !strings.Contains(view, "(1/1 terms)") {
		t.Errorf("TUI did not use canonical term denominator:\n%s", view)
	}
}

func TestParseArguments_StartDir_Accepted(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir", "/home/user/project", "bar"})
	expected := filepath.Clean("/home/user/project")
	if args.StartDir != expected {
		t.Errorf("expected StartDir=%q, got %q", expected, args.StartDir)
	}
	if len(args.SearchWords) != 2 {
		t.Errorf("expected 2 search words, got %v", args.SearchWords)
	}
}

func TestParseArguments_StartDir_NormalizesTrailingSlash(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir", "/home/user/project/", "bar"})
	// filepath.Clean should remove trailing slash
	if args.StartDir == "/home/user/project/" {
		t.Errorf("expected trailing slash to be cleaned, got %q", args.StartDir)
	}
}

func TestParseArguments_StartDir_RejectsWildcard(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir", "/home/user/proj*"})
	// Invalid startdir should not be stored; StartDirErr should be set
	if args.StartDirErr == nil {
		t.Error("expected StartDirErr to be set for wildcard path, got nil")
	}
}

func TestParseArguments_StartDir_RejectsSemicolon(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir", "/home;bad"})
	if args.StartDirErr == nil {
		t.Error("expected StartDirErr to be set for semicolon injection, got nil")
	}
}

func TestParseArguments_StartDir_NotProvided(t *testing.T) {
	args := parseArguments([]string{"foo", "bar"})
	if args.StartDir != "" {
		t.Errorf("expected empty StartDir when not provided, got %q", args.StartDir)
	}
	if args.StartDirErr != nil {
		t.Errorf("expected nil StartDirErr when not provided, got %v", args.StartDirErr)
	}
}

func TestParseArguments_PathScope_Accepted(t *testing.T) {
	args := parseArguments([]string{"foo", "--pathscope", "*/backend/*,tests/*"})
	if len(args.PathScope) != 2 {
		t.Errorf("expected 2 path scope segments, got %v", args.PathScope)
	}
	if args.PathScope[0] != "*/backend/*" {
		t.Errorf("expected first segment '*/backend/*', got %q", args.PathScope[0])
	}
	if args.PathScope[1] != "tests/*" {
		t.Errorf("expected second segment 'tests/*', got %q", args.PathScope[1])
	}
}

func TestParseArguments_PathScope_RejectsExtension(t *testing.T) {
	// Dots are valid in pathscope -- treated as part of dir/filename.
	// garp always runs; CLI help warns users to use --not/--only instead.
	args := parseArguments([]string{"foo", "--pathscope", "*/backend/*.cs"})
	if args.PathScopeErr != nil {
		t.Errorf("unexpected PathScopeErr for dotted segment: %v", args.PathScopeErr)
	}
	if len(args.PathScope) != 1 {
		t.Errorf("expected 1 scope segment, got %v", args.PathScope)
	}
}

func TestParseArguments_PathScope_RejectsRegex(t *testing.T) {
	args := parseArguments([]string{"foo", "--pathscope", "*/backend/[Aa]ssembly"})
	if args.PathScopeErr == nil {
		t.Error("expected PathScopeErr for regex bracket, got nil")
	}
}

func TestParseArguments_PathScope_NotProvided(t *testing.T) {
	args := parseArguments([]string{"foo", "bar"})
	if len(args.PathScope) != 0 {
		t.Errorf("expected empty PathScope when not provided, got %v", args.PathScope)
	}
	if args.PathScopeErr != nil {
		t.Errorf("expected nil PathScopeErr when not provided, got %v", args.PathScopeErr)
	}
}

func TestParseArguments_BothFlags_WorkTogether(t *testing.T) {
	args := parseArguments([]string{
		"assembly", "main",
		"--startdir", "/home/user/project",
		"--pathscope", "*/backend/*/Assembly",
		"--code",
	})
	expectedStartDir := filepath.Clean("/home/user/project")
	if args.StartDir != expectedStartDir {
		t.Errorf("unexpected StartDir: %q", args.StartDir)
	}
	if len(args.PathScope) != 1 || args.PathScope[0] != "*/backend/*/Assembly" {
		t.Errorf("unexpected PathScope: %v", args.PathScope)
	}
	if !args.IncludeCode {
		t.Error("expected IncludeCode=true")
	}
	if len(args.SearchWords) != 2 {
		t.Errorf("expected 2 search words, got %v", args.SearchWords)
	}
}

func TestParseArguments_StartDir_WithEquals(t *testing.T) {
	args := parseArguments([]string{"foo", "--startdir=/home/user/project"})
	if args.ParseErr != nil {
		t.Fatalf("ParseErr = %v, want nil", args.ParseErr)
	}
	if want := filepath.Clean("/home/user/project"); args.StartDir != want {
		t.Errorf("StartDir = %q, want %q", args.StartDir, want)
	}
}

func TestParseArgumentsStartDirs(t *testing.T) {
	args := parseArguments([]string{
		"needle",
		"--startdir", "first, second",
	})
	if args.ParseErr != nil {
		t.Fatal(args.ParseErr)
	}
	if !args.StartDirProvided {
		t.Fatal("StartDirProvided = false")
	}
	if len(args.StartDirs) != 1 || args.StartDirs[0] != "first, second" {
		t.Fatalf("StartDirs = %q", args.StartDirs)
	}
	if args.StartDir != filepath.Clean("first") {
		t.Fatalf("StartDir = %q, want first compatibility value", args.StartDir)
	}
}

func TestParseArgumentsStrict_DefaultsFalse(t *testing.T) {
	args := parseArguments([]string{"term1", "term2", "term3"})
	if args.Strict {
		t.Error("Strict = true without --strict, want false")
	}
}

func TestParseArgumentsStrict_Enabled(t *testing.T) {
	args := parseArguments([]string{"term1", "term2", "term3", "--strict"})
	if !args.Strict {
		t.Error("Strict = false with --strict, want true")
	}
}

func TestParseArgumentsStrict_CombinedWithOutputAndSearchFlags(t *testing.T) {
	args := parseArguments([]string{
		"term1", "term2", "term3",
		"--strict", "--json", "--plain", "--code", "--distance", "321",
	})

	if !args.Strict {
		t.Error("Strict = false with --strict, want true")
	}
	if !args.JSONOutput {
		t.Error("JSONOutput = false with --json, want true")
	}
	if !args.PlainOutput {
		t.Error("PlainOutput = false with --plain, want true")
	}
	if !args.IncludeCode {
		t.Error("IncludeCode = false with --code, want true")
	}
	if args.Distance != 321 {
		t.Errorf("Distance = %d, want 321", args.Distance)
	}
}

func TestParseArgumentsStrict(t *testing.T) {
	t.Run("space and equals value forms", func(t *testing.T) {
		space := parseArguments([]string{
			"needle", "--distance", "12", "--workers", "3", "--heavy-concurrency", "2",
			"--file-timeout-binary", "900", "--max-excerpts", "4",
		})
		equals := parseArguments([]string{
			"needle", "--distance=12", "--workers=3", "--heavy-concurrency=2",
			"--file-timeout-binary=900", "--max-excerpts=4",
		})
		if space.ParseErr != nil || equals.ParseErr != nil {
			t.Fatalf("ParseErr = %v / %v, want nil", space.ParseErr, equals.ParseErr)
		}
		if space.Distance != equals.Distance ||
			space.FilterWorkers != equals.FilterWorkers ||
			space.HeavyConcurrency != equals.HeavyConcurrency ||
			space.FileTimeoutBinary != equals.FileTimeoutBinary ||
			space.MaxExcerpts != equals.MaxExcerpts {
			t.Fatalf("space and equals forms differ: %#v / %#v", space, equals)
		}
	})

	tests := []struct {
		name string
		argv []string
		want string
	}{
		{name: "unknown flag", argv: []string{"needle", "--bogus"}, want: "unknown flag"},
		{name: "unknown flag after not", argv: []string{"needle", "--not", "--bogus"}, want: "unknown flag"},
		{name: "missing value", argv: []string{"needle", "--workers"}, want: "--workers"},
		{name: "empty equals value", argv: []string{"needle", "--only="}, want: "--only"},
		{name: "empty separated numeric value", argv: []string{"needle", "--workers", ""}, want: "missing value"},
		{name: "empty separated path value", argv: []string{"needle", "--startdir", ""}, want: "missing value"},
		{name: "empty separated type value", argv: []string{"needle", "--only", ""}, want: "missing value"},
		{name: "dash-leading separated value", argv: []string{"needle", "--startdir", "-dir"}, want: "dash-leading"},
		{name: "boolean attached value", argv: []string{"needle", "--code=true"}, want: "does not accept"},
		{name: "info attached value", argv: []string{"needle", "--help=true"}, want: "does not accept"},
		{name: "invalid positive integer", argv: []string{"needle", "--distance=0"}, want: "positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseArguments(test.argv)
			if got.ParseErr == nil || !strings.Contains(got.ParseErr.Error(), test.want) {
				t.Fatalf("ParseErr = %v, want error containing %q", got.ParseErr, test.want)
			}
		})
	}

	t.Run("recognized flags remain active after not", func(t *testing.T) {
		got := parseArguments([]string{"needle=a", "--not", "excluded=b", "--json", "--code"})
		if got.ParseErr != nil {
			t.Fatalf("ParseErr = %v, want nil", got.ParseErr)
		}
		if !got.JSONOutput || !got.IncludeCode {
			t.Fatalf("flags after --not were not parsed: %#v", got)
		}
		if !slicesEqual(got.SearchWords, []string{"needle=a"}) {
			t.Fatalf("SearchWords = %q, want literal equals term", got.SearchWords)
		}
		if len(got.ExcludeWords) != 1 || got.ExcludeWords[0] != "excluded=b" {
			t.Fatalf("ExcludeWords = %q, want [excluded=b]", got.ExcludeWords)
		}
	})

	t.Run("dash-leading equals value reaches validator", func(t *testing.T) {
		got := parseArguments([]string{"needle", "--startdir=-dir"})
		if got.ParseErr != nil {
			t.Fatalf("ParseErr = %v, want nil", got.ParseErr)
		}
		if got.StartDir != "-dir" {
			t.Fatalf("StartDir = %q, want -dir", got.StartDir)
		}
	})

	t.Run("separated negative integers reach numeric validators", func(t *testing.T) {
		for _, flag := range []string{
			"--distance",
			"-distance",
			"--heavy-concurrency",
			"--file-timeout-binary",
			"--workers",
			"-workers",
			"--max-excerpts",
			"-m",
			"--max-count",
			"--max-results",
			"--max-filesize",
		} {
			separated := parseArguments([]string{"needle", flag, "-1"})
			attached := parseArguments([]string{"needle", flag + "=-1"})
			if separated.ParseErr == nil || attached.ParseErr == nil {
				t.Errorf("%s: errors = %v / %v, want both", flag, separated.ParseErr, attached.ParseErr)
				continue
			}
			if separated.ParseErr.Error() != attached.ParseErr.Error() {
				t.Errorf("%s: separated error %q, attached error %q", flag, separated.ParseErr, attached.ParseErr)
			}
		}
	})

	t.Run("non-numeric dash token remains a flag", func(t *testing.T) {
		arbitrary := parseArguments([]string{"needle", "--workers", "-oops"})
		if arbitrary.ParseErr == nil || !strings.Contains(arbitrary.ParseErr.Error(), "missing value") {
			t.Fatalf("arbitrary dash token error = %v", arbitrary.ParseErr)
		}

		actualFlag := parseArguments([]string{"needle", "--workers", "--json"})
		if actualFlag.ParseErr == nil || !strings.Contains(actualFlag.ParseErr.Error(), "missing value") {
			t.Fatalf("actual flag error = %v", actualFlag.ParseErr)
		}
		if !actualFlag.JSONOutput {
			t.Fatal("--json was consumed as the --workers value")
		}
	})

	t.Run("Run reports parser error first", func(t *testing.T) {
		missingRoot := filepath.Join(t.TempDir(), "missing")
		code, stderr := runWithArgsAndStderr(t, []string{"garp", "--bogus", "--startdir", missingRoot})
		if code != 1 {
			t.Fatalf("Run() = %d, want 1", code)
		}
		if !strings.Contains(stderr, "unknown flag") {
			t.Fatalf("stderr = %q, want parser error", stderr)
		}
		if strings.Contains(stderr, "invalid search root") {
			t.Fatalf("stderr = %q, root I/O ran before parser error", stderr)
		}
	})

	t.Run("first error survives later valid values", func(t *testing.T) {
		numeric := parseArguments([]string{"needle", "--workers", "bad", "--workers", "3"})
		if numeric.ParseErr == nil || !strings.Contains(numeric.ParseErr.Error(), "--workers") {
			t.Fatalf("numeric ParseErr = %v, want first workers error", numeric.ParseErr)
		}
		partial := parseArguments([]string{"needle", "--partial=bad", "--partial=prefix"})
		if partial.ParseErr == nil || !strings.Contains(partial.ParseErr.Error(), "--partial") {
			t.Fatalf("partial ParseErr = %v, want first partial error", partial.ParseErr)
		}
	})

	t.Run("info flags cannot bypass parser errors", func(t *testing.T) {
		for _, infoFlag := range []string{"--help", "--version"} {
			for _, argv := range [][]string{
				{"garp", "--bogus", infoFlag},
				{"garp", infoFlag, "--bogus"},
			} {
				code, stderr := runWithArgsAndStderr(t, argv)
				if code != 1 {
					t.Errorf("%v: Run() = %d, want 1", argv[1:], code)
				}
				if !strings.Contains(stderr, "unknown flag") {
					t.Errorf("%v: stderr = %q, want parser error", argv[1:], stderr)
				}
			}
		}
	})

	t.Run("info flags retain normal behavior", func(t *testing.T) {
		previousArgs := os.Args
		defer func() { os.Args = previousArgs }()
		for _, test := range []struct {
			flag string
			want string
		}{
			{flag: "--help", want: "USAGE"},
			{flag: "--version", want: "garp v"},
		} {
			os.Args = []string{"garp", test.flag}
			code := -1
			stdout := string(captureCLIStdout(t, func() int {
				code = Run()
				return code
			}))
			if code != 0 {
				t.Errorf("%s: Run() = %d, want 0", test.flag, code)
			}
			if !strings.Contains(stdout, test.want) {
				t.Errorf("%s: stdout missing %q", test.flag, test.want)
			}
		}
	})
}

func TestParseArgumentsUserGlobs(t *testing.T) {
	args := parseArguments([]string{
		"needle",
		"-g", "*.go",
		"--glob=!vendor/**",
		"--iglob", "README.*",
		"--glob", "docs/a,b/*.md",
	})
	if args.ParseErr != nil {
		t.Fatalf("ParseErr = %v", args.ParseErr)
	}
	want := []search.UserGlob{
		{Pattern: "*.go"},
		{Pattern: "vendor/**", Negated: true},
		{Pattern: "README.*", IgnoreCase: true},
		{Pattern: "docs/a,b/*.md"},
	}
	if len(args.UserGlobs) != len(want) {
		t.Fatalf("UserGlobs = %+v, want %+v", args.UserGlobs, want)
	}
	for i := range want {
		if args.UserGlobs[i] != want[i] {
			t.Errorf("UserGlobs[%d] = %+v, want %+v", i, args.UserGlobs[i], want[i])
		}
	}
}

func TestRootPolicyFlagOccurrences(t *testing.T) {
	tests := []struct {
		flag         string
		firstValue   string
		secondValue  string
		wantGuidance string
	}{
		{
			flag:         "--startdir",
			firstValue:   "first",
			secondValue:  "second",
			wantGuidance: "comma-separated paths",
		},
		{
			flag:         "--pathscope",
			firstValue:   "src",
			secondValue:  "docs",
			wantGuidance: "repeat -g",
		},
	}
	for _, test := range tests {
		t.Run(test.flag, func(t *testing.T) {
			forms := [][]string{
				{"needle", test.flag, test.firstValue, test.flag, test.secondValue},
				{"needle", test.flag + "=" + test.firstValue, test.flag + "=" + test.secondValue},
				{"needle", test.flag, test.firstValue, test.flag + "=" + test.secondValue},
				{"needle", test.flag + "=" + test.firstValue, test.flag, test.secondValue},
			}
			for _, argv := range forms {
				args := parseArguments(argv)
				if args.ParseErr == nil {
					t.Fatalf("%v: ParseErr = nil, want repeated flag rejection", argv)
				}
				message := args.ParseErr.Error()
				if !strings.Contains(message, test.flag+" may only be specified once") {
					t.Fatalf("%v: ParseErr = %q, want repeat description", argv, message)
				}
				if !strings.Contains(message, test.wantGuidance) {
					t.Fatalf("%v: ParseErr = %q, want guidance %q", argv, message, test.wantGuidance)
				}
			}

			code, stderr := runWithArgsAndStderr(t, []string{
				"garp", "needle", test.flag, test.firstValue, test.flag + "=" + test.secondValue,
			})
			if code != 1 {
				t.Fatalf("Run() = %d, want 1", code)
			}
			if !strings.Contains(stderr, test.flag+" may only be specified once") {
				t.Fatalf("stderr = %q, want repeated flag error", stderr)
			}
		})
	}

	t.Run("comma-separated values remain valid", func(t *testing.T) {
		args := parseArguments([]string{
			"needle",
			"--startdir", "first, second",
			"--pathscope=src,docs/**",
		})
		if args.ParseErr != nil {
			t.Fatalf("ParseErr = %v, want nil", args.ParseErr)
		}
		if !slicesEqual(args.StartDirs, []string{"first, second"}) {
			t.Fatalf("StartDirs = %q, want one comma-list occurrence", args.StartDirs)
		}
		if !slicesEqual(args.PathScope, []string{"src", "docs/**"}) {
			t.Fatalf("PathScope = %q, want comma-separated patterns", args.PathScope)
		}
	})

	t.Run("globs remain repeatable", func(t *testing.T) {
		args := parseArguments([]string{
			"needle",
			"-g", "*.go",
			"--glob=docs/**",
			"--iglob", "README.*",
		})
		if args.ParseErr != nil {
			t.Fatalf("ParseErr = %v, want nil", args.ParseErr)
		}
		if len(args.UserGlobs) != 3 {
			t.Fatalf("UserGlobs = %+v, want three repeated globs", args.UserGlobs)
		}
	})
}

func TestParseArgumentsUserGlobErrorUsesActualAliasOnce(t *testing.T) {
	for _, flag := range []string{"-g", "--glob", "--iglob"} {
		t.Run(flag, func(t *testing.T) {
			args := parseArguments([]string{"needle", flag, "!"})
			want := flag + ": user glob negation must include a pattern"
			if args.ParseErr == nil || args.ParseErr.Error() != want {
				t.Fatalf("ParseErr = %q, want %q", args.ParseErr, want)
			}
		})
	}
}

func TestParseArgumentsRejectsBareDirectoryMarkers(t *testing.T) {
	tests := []struct {
		flag  string
		value string
	}{
		{flag: "--pathscope", value: "/"},
		{flag: "--pathscope", value: `\`},
		{flag: "-g", value: "/"},
		{flag: "--glob", value: "!/"},
		{flag: "--iglob", value: `\`},
	}
	for _, tt := range tests {
		t.Run(tt.flag+"="+tt.value, func(t *testing.T) {
			args := parseArguments([]string{"needle", tt.flag, tt.value})
			if args.ParseErr == nil {
				t.Fatalf("parseArguments accepted %s %q", tt.flag, tt.value)
			}
			if !strings.Contains(args.ParseErr.Error(), tt.flag) {
				t.Fatalf("ParseErr = %q, want flag context %q", args.ParseErr, tt.flag)
			}
		})
	}
}

func TestRunRejectsInvalidGlobBeforeRootPreflight(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	code, stderr := runWithArgsAndStderr(t, []string{
		"garp", "needle", "--glob", "!", "--startdir", missing,
	})
	if code != 1 {
		t.Fatalf("Run() = %d, want 1", code)
	}
	if !strings.Contains(stderr, "--glob") {
		t.Fatalf("stderr = %q, want glob validation error", stderr)
	}
	if strings.Contains(stderr, "search root") || strings.Contains(stderr, missing) {
		t.Fatalf("stderr = %q, root preflight ran before glob validation", stderr)
	}
}

func TestParseArgumentsSeparator(t *testing.T) {
	got := parseArguments([]string{"term", "--", "-v", "--bogus", "-C3"})
	if got.ParseErr != nil {
		t.Fatalf("ParseErr = %v, want nil", got.ParseErr)
	}
	want := []string{"term", "-v", "--bogus", "-c3"}
	if !slicesEqual(got.SearchWords, want) {
		t.Fatalf("SearchWords = %q, want %q", got.SearchWords, want)
	}

	excludes := parseArguments([]string{"term", "--not", "first", "--", "--json", "-C3"})
	if excludes.ParseErr != nil {
		t.Fatalf("ParseErr = %v, want nil", excludes.ParseErr)
	}
	if !slicesEqual(excludes.ExcludeWords, []string{"first", "--json", "-C3"}) {
		t.Fatalf("ExcludeWords = %q, want literal tokens", excludes.ExcludeWords)
	}
	if excludes.JSONOutput {
		t.Fatal("JSONOutput = true, flag after -- must be literal")
	}
}

func TestParseArgumentsContextFlags(t *testing.T) {
	for _, token := range []string{
		"-C", "-C3", "-C=3", "--context", "--context=3",
		"-A", "-A2", "-A=2", "--after-context", "--after-context=2",
		"-B", "-B2", "-B=2", "--before-context", "--before-context=2",
	} {
		t.Run(token, func(t *testing.T) {
			got := parseArguments([]string{"needle", token, "3"})
			if got.ParseErr == nil {
				t.Fatal("ParseErr = nil, want unsupported-context error")
			}
			message := got.ParseErr.Error()
			if !strings.Contains(message, "--distance") || !strings.Contains(message, "--max-excerpts") {
				t.Fatalf("ParseErr = %q, want context guidance", message)
			}
		})
	}
}

func TestParseArgumentsPartialContract(t *testing.T) {
	got := parseArguments([]string{"--partial", "prefix"})
	if got.ParseErr != nil {
		t.Fatalf("ParseErr = %v, want nil", got.ParseErr)
	}
	if got.Partial != search.PartialModePrefix || !slicesEqual(got.SearchWords, []string{"prefix"}) {
		t.Fatalf("bare partial consumed its following term: %#v", got)
	}

	for _, mode := range []string{"off", "none"} {
		got = parseArguments([]string{"needle", "--partial=" + mode})
		if got.ParseErr != nil || got.Partial != search.PartialModeOff {
			t.Fatalf("--partial=%s: Partial = %q, ParseErr = %v", mode, got.Partial, got.ParseErr)
		}
	}

	got = parseArguments([]string{"needle", "--partial="})
	if got.ParseErr == nil || got.PartialErr == nil {
		t.Fatalf("empty partial mode errors = %v / %v, want both set", got.ParseErr, got.PartialErr)
	}
}

func TestParseArgumentsAliases(t *testing.T) {
	got := parseArguments([]string{
		"Needle", "-l", "--files-with-matches", "-m", "80", "--max-count=2",
	})
	if got.ParseErr != nil {
		t.Fatalf("ParseErr = %v", got.ParseErr)
	}
	if !got.PathOnly {
		t.Fatal("path-only aliases did not set PathOnly")
	}
	if got.MaxExcerpts != 2 {
		t.Fatalf("MaxExcerpts = %d, want last value 2", got.MaxExcerpts)
	}
	if got.SearchWords[0] != "needle" {
		t.Fatalf("SearchWords = %q, want lowercase default", got.SearchWords)
	}

	clamped := parseArguments([]string{"needle", "-m=51"})
	if clamped.ParseErr != nil || clamped.MaxExcerpts != 50 {
		t.Fatalf("-m=51 = excerpts %d, error %v; want 50, nil", clamped.MaxExcerpts, clamped.ParseErr)
	}
	for _, argv := range [][]string{
		{"needle", "-m", "0"},
		{"needle", "--max-count=bad"},
		{"needle", "-l=true"},
	} {
		if parsed := parseArguments(argv); parsed.ParseErr == nil {
			t.Errorf("%q: ParseErr = nil", argv)
		}
	}
}

func TestParseArgumentsOnlyTypes(t *testing.T) {
	got := parseArguments([]string{
		"needle", "-t", ".PDF,md", "--type=pdf", "--only", "DockerFile,+custom",
	})
	if got.ParseErr != nil {
		t.Fatalf("ParseErr = %v", got.ParseErr)
	}
	want := []string{"pdf", "md", "dockerfile", "+custom"}
	if !slicesEqual(got.OnlyTypes, want) || got.OnlyType != "pdf" {
		t.Fatalf("types = %q, singular = %q; want %q, pdf", got.OnlyTypes, got.OnlyType, want)
	}

	for _, value := range []string{"", "pdf,,md", "..pdf", "c#"} {
		parsed := parseArguments([]string{"needle", "--type=" + value})
		if parsed.ParseErr == nil {
			t.Errorf("--type=%q: ParseErr = nil", value)
		}
	}
}

func TestParseArgumentsCaseMode(t *testing.T) {
	sensitive := parseArguments([]string{"API", "api", "-i", "-s"})
	if sensitive.ParseErr != nil || !sensitive.CaseSensitive {
		t.Fatalf("sensitive parse = %#v", sensitive)
	}
	if !slicesEqual(sensitive.SearchWords, []string{"API", "api"}) {
		t.Fatalf("case-sensitive terms = %q", sensitive.SearchWords)
	}

	insensitive := parseArguments([]string{"API", "api", "--case-sensitive", "--ignore-case"})
	if insensitive.ParseErr != nil || insensitive.CaseSensitive {
		t.Fatalf("insensitive parse = %#v", insensitive)
	}
	if !slicesEqual(insensitive.SearchWords, []string{"api"}) {
		t.Fatalf("case-insensitive terms = %q", insensitive.SearchWords)
	}
}
