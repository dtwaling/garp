package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"garp/search"
)

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
