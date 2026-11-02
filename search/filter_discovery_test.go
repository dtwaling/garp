package search

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestDiscoveryByteScanner(t *testing.T) {
	tests := []struct {
		name string
		buf  string
		word string
		mode PartialMode
		want bool
	}{
		{
			name: "whole word mode rejects derivative",
			buf:  "deployment",
			word: "deploy",
			mode: PartialModeOff,
			want: false,
		},
		{
			name: "prefix mode accepts derivative",
			buf:  "deployment",
			word: "deploy",
			mode: PartialModePrefix,
			want: true,
		},
		{
			name: "glob enables prefix while mode is off",
			buf:  "deployment",
			word: "deploy*",
			mode: PartialModeOff,
			want: true,
		},
		{
			name: "contains mode accepts derivative",
			buf:  "deployment",
			word: "deploy",
			mode: PartialModeContains,
			want: true,
		},
		{
			name: "prefix mode rejects internal substring",
			buf:  "redeploy",
			word: "deploy",
			mode: PartialModePrefix,
			want: false,
		},
		{
			name: "glob rejects internal substring",
			buf:  "redeploy",
			word: "deploy*",
			mode: PartialModeOff,
			want: false,
		},
		{
			name: "contains mode accepts internal substring",
			buf:  "redeploy",
			word: "deploy",
			mode: PartialModeContains,
			want: true,
		},
		{
			name: "prefix mode accepts snake case token",
			buf:  "deploy_service",
			word: "deploy",
			mode: PartialModePrefix,
			want: true,
		},
		{
			name: "contains mode accepts snake case token",
			buf:  "deploy_service",
			word: "deploy",
			mode: PartialModeContains,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := asciiIndexPartialCI([]byte(tt.buf), []byte(tt.word), tt.mode) >= 0
			if got != tt.want {
				t.Errorf("asciiIndexPartialCI(%q, %q, %q) found = %t, want %t", tt.buf, tt.word, tt.mode, got, tt.want)
			}
		})
	}
}

func TestDiscoveryPlainASCIIFastPathAgreesWithMatcher(t *testing.T) {
	tests := []struct {
		name string
		term string
		mode PartialMode
		text string
		from int
	}{
		{name: "off exact", term: "service", text: "a SERVICE."},
		{name: "off plural", term: "service", text: "a services."},
		{name: "off es suffix", term: "process", text: "processes complete"},
		{name: "off rejects derivative", term: "deploy", text: "deployment"},
		{name: "off leading boundary", term: "service", text: "xservice service"},
		{name: "prefix derivative", term: "deploy", mode: PartialModePrefix, text: "deployment"},
		{name: "prefix underscore boundary", term: "service", mode: PartialModePrefix, text: "my_service"},
		{name: "strict prefix boundary", term: "foo-bar", mode: PartialModePrefix, text: "_foo-barValue"},
		{name: "contains internal", term: "deploy", mode: PartialModeContains, text: "redeployment"},
		{name: "nonword trailing boundary", term: "ms-", text: "ms-option"},
		{name: "offset retains prior context", term: "service", text: "xservice service", from: 1},
		{name: "ordinary UTF-8 chunk", term: "service", text: "café service"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, tt.mode, true)
			found, ok := matcher.find(tt.text, tt.from)
			want := -1
			if ok {
				want = found.start
			}
			if got := discoveryMatchIndex([]byte(tt.text), matcher, tt.from); got != want {
				t.Fatalf("fast path index = %d, matcher index = %d", got, want)
			}
		})
	}
}

func TestDiscoveryPlainASCIIPathSelection(t *testing.T) {
	tests := []struct {
		name string
		term string
		text string
		want bool
	}{
		{name: "ASCII", term: "service", text: "ordinary service", want: true},
		{name: "ordinary UTF-8", term: "service", text: "café service", want: true},
		{name: "long s", term: "service", text: "ſervice", want: false},
		{name: "Kelvin sign", term: "kernel", text: "Kernel", want: false},
		{name: "non-ASCII term", term: "CAFÉ", text: "café", want: false},
		{name: "wildcard", term: "ms-*-api", text: "ms-option-api", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, PartialModeOff, true)
			if got := canUseASCIIPlainTermFastPath([]byte(tt.text), matcher); got != tt.want {
				t.Fatalf("fast-path eligibility = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestDiscoveryMatcherFallbackAgreement(t *testing.T) {
	tests := []struct {
		name string
		term string
		text string
	}{
		{name: "Unicode term", term: "CAFÉ", text: "café service"},
		{name: "long s fold", term: "service", text: "ſervice"},
		{name: "Kelvin fold", term: "kernel", text: "Kernel"},
		{name: "wildcard", term: "ms-*-api", text: "ms-option-api"},
		{name: "invalid wildcard", term: "a*", text: " a* "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, PartialModeOff, true)
			found, ok := matcher.find(tt.text, 0)
			want := -1
			if ok {
				want = found.start
			}
			if got := discoveryMatchIndex([]byte(tt.text), matcher, 0); got != want {
				t.Fatalf("discovery index = %d, matcher index = %d", got, want)
			}
		})
	}
}

func TestDiscoveryPlainASCIIFastPathAllocations(t *testing.T) {
	matcher := compileTermMatcher("needle", PartialModeOff, true)
	for _, tt := range []struct {
		name string
		buf  []byte
	}{
		{name: "ASCII", buf: []byte(strings.Repeat("ordinary ascii content ", 64) + "needle.")},
		{name: "ordinary UTF-8", buf: []byte(strings.Repeat("café content ", 64) + "needle.")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var index int
			allocations := testing.AllocsPerRun(1000, func() {
				index = discoveryMatchIndex(tt.buf, matcher, 0)
			})
			if index < 0 {
				t.Fatal("plain ASCII fast path did not find the term")
			}
			if allocations != 0 {
				t.Fatalf("plain ASCII discovery allocations = %v, want 0", allocations)
			}
		})
	}
}

func TestDiscoveryByteScannerFileWalk(t *testing.T) {
	root := t.TempDir()
	derivative := filepath.Join(root, "derivative.md")
	internal := filepath.Join(root, "internal.md")
	if err := os.WriteFile(derivative, []byte("deployment is ready"), 0o644); err != nil {
		t.Fatalf("write derivative fixture: %v", err)
	}
	if err := os.WriteFile(internal, []byte("redeploy is ready"), 0o644); err != nil {
		t.Fatalf("write internal fixture: %v", err)
	}

	for _, tt := range []struct {
		name      string
		word      string
		mode      PartialMode
		wantFiles []string
	}{
		{"prefix", "deploy", PartialModePrefix, []string{"derivative.md"}},
		{"glob", "deploy*", PartialModeOff, []string{"derivative.md"}},
		{"contains", "deploy", PartialModeContains, []string{"derivative.md", "internal.md"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files, err := FindFilesWithFirstWordPartial(tt.word, nil, root, nil, tt.mode)
			if err != nil {
				t.Fatalf("FindFilesWithFirstWordPartial: %v", err)
			}
			got := make(map[string]bool, len(files))
			for _, file := range files {
				got[filepath.Base(file)] = true
			}
			if len(got) != len(tt.wantFiles) {
				t.Fatalf("candidate count = %d, want %d: %#v", len(got), len(tt.wantFiles), got)
			}
			for _, want := range tt.wantFiles {
				if !got[want] {
					t.Errorf("missing candidate %q in %#v", want, got)
				}
			}

			progressFiles, err := FindFilesWithFirstWordProgressPartial([]string{tt.word}, nil, 1, nil, root, nil, tt.mode)
			if err != nil {
				t.Fatalf("FindFilesWithFirstWordProgressPartial: %v", err)
			}
			progressGot := make(map[string]bool, len(progressFiles))
			for _, file := range progressFiles {
				progressGot[filepath.Base(file)] = true
			}
			if len(progressGot) != len(tt.wantFiles) {
				t.Fatalf("progress candidate count = %d, want %d: %#v", len(progressGot), len(tt.wantFiles), progressGot)
			}
			for _, want := range tt.wantFiles {
				if !progressGot[want] {
					t.Errorf("progress scan missing candidate %q in %#v", want, progressGot)
				}
			}
		})
	}
}

func TestFindFilesSerialInlineWildcard(t *testing.T) {
	testInlineWildcardDiscovery(t, false)
}

func TestFindFilesProgressInlineWildcard(t *testing.T) {
	testInlineWildcardDiscovery(t, true)
}

func testInlineWildcardDiscovery(t *testing.T, progress bool) {
	t.Helper()

	tests := []struct {
		name     string
		term     string
		contents map[string]string
		want     []string
	}{
		{
			name: "internal star",
			term: "ms-*-api",
			contents: map[string]string{
				"option.md":     "ms-option-api",
				"qualified.md":  "dev-ms-option-api-alb",
				"dotted.md":     "ms-core.v1-api",
				"camel.md":      "MsOptionApiClient",
				"underscore.md": "ms_option_api",
				"missing.md":    "ms-core.api",
				"suffix.md":     "ms-option-apis",
			},
			want: []string{"dotted.md", "option.md", "qualified.md"},
		},
		{
			name: "surrounding stars",
			term: "*OptionApi*",
			contents: map[string]string{
				"client.md":  "MsOptionApiClient",
				"default.md": "defaultMsOptionApiOrigin",
				"other.md":   "option service api",
			},
			want: []string{"client.md", "default.md"},
		},
		{
			name: "leading internal and trailing stars",
			term: "*ms-*-api*",
			contents: map[string]string{
				"url.md":   "https://dev-ms-option-api-alb.ihmsweb.com/api/v1",
				"other.md": "https://dev-ms-option-alb.ihmsweb.com/api/v1",
			},
			want: []string{"url.md"},
		},
		{
			name: "single character question",
			term: "ms-?-api",
			contents: map[string]string{
				"letter.md": "ms-a-api",
				"number.md": "ms-1-api",
				"zero.md":   "ms--api",
				"many.md":   "ms-option-api",
			},
			want: []string{"letter.md", "number.md"},
		},
		{
			name: "question inside word",
			term: "mark?systems",
			contents: map[string]string{
				"hyphen.md": "mark-systems",
				"dot.md":    "mark.systems",
				"zero.md":   "marksystems",
				"many.md":   "markXXsystems",
			},
			want: []string{"dot.md", "hyphen.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range tt.contents {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}

			got := runDiscovery(t, root, tt.term, progress)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("candidates = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindFilesSerialWildcardChunkBoundary(t *testing.T) {
	testWildcardChunkBoundaryDiscovery(t, false)
}

func TestFindFilesProgressWildcardChunkBoundary(t *testing.T) {
	testWildcardChunkBoundaryDiscovery(t, true)
}

func testWildcardChunkBoundaryDiscovery(t *testing.T, progress bool) {
	t.Helper()

	const chunkSize = 64 * 1024
	maxTerm := strings.Repeat("a", 64) + "*" + strings.Repeat("?", 16) + strings.Repeat("b", 64)
	tests := []struct {
		name  string
		term  string
		match string
	}{
		{
			name:  "120 byte star expansion",
			term:  "a*b",
			match: "a" + strings.Repeat("x", 120) + "b",
		},
		{
			name: "272 byte maximum with leading boundary context",
			term: maxTerm,
			match: strings.Repeat("a", 64) +
				strings.Repeat("x", 128) +
				strings.Repeat("q", 16) +
				strings.Repeat("b", 64),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			matchStart := chunkSize - len(tt.match) + 1
			prefix := strings.Repeat("z", matchStart-1)
			suffix := strings.Repeat("z", 1024)

			qualified := prefix + "." + tt.match + "." + suffix
			attached := prefix + "x" + tt.match + "." + suffix
			if err := os.WriteFile(filepath.Join(root, "qualified.md"), []byte(qualified), 0o644); err != nil {
				t.Fatalf("write qualified fixture: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "attached.md"), []byte(attached), 0o644); err != nil {
				t.Fatalf("write attached fixture: %v", err)
			}

			got := runDiscovery(t, root, tt.term, progress)
			want := []string{"qualified.md"}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("candidates = %q, want %q", got, want)
			}
		})
	}
}

func TestFindFilesSerialPlainChunkBoundary(t *testing.T) {
	testPlainChunkBoundaryDiscovery(t, false)
}

func TestFindFilesProgressPlainChunkBoundary(t *testing.T) {
	testPlainChunkBoundaryDiscovery(t, true)
}

func testPlainChunkBoundaryDiscovery(t *testing.T, progress bool) {
	t.Helper()

	const chunkSize = 64 * 1024
	term := strings.Repeat("a", 80)
	root := t.TempDir()
	prefix := strings.Repeat("z", chunkSize-40-1)
	content := prefix + "." + term + "." + strings.Repeat("z", 1024)
	if err := os.WriteFile(filepath.Join(root, "qualified.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write qualified fixture: %v", err)
	}

	got := runDiscovery(t, root, term, progress)
	want := []string{"qualified.md"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
}

func TestFindFilesSerialRejectsArtificialChunkBoundary(t *testing.T) {
	testRejectsArtificialChunkBoundary(t, false)
}

func TestFindFilesProgressRejectsArtificialChunkBoundary(t *testing.T) {
	testRejectsArtificialChunkBoundary(t, true)
}

func testRejectsArtificialChunkBoundary(t *testing.T, progress bool) {
	t.Helper()

	const chunkSize = 64 * 1024
	term := strings.Repeat("a", 40)
	matcher := compileTermMatcher(term, PartialModeOff, true)
	overlap := discoveryMatcherOverlap(matcher)
	matchStart := chunkSize - overlap
	content := strings.Repeat("z", matchStart-1) +
		"x" + term + "." + strings.Repeat("z", 1024)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "attached.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write attached fixture: %v", err)
	}
	if got := runDiscovery(t, root, term, progress); len(got) != 0 {
		t.Fatalf("artificial chunk boundary produced candidates %q", got)
	}
}

func runDiscovery(t *testing.T, root, term string, progress bool) []string {
	t.Helper()

	var (
		files []string
		err   error
	)
	if progress {
		files, err = FindFilesWithFirstWordProgressPartial(
			[]string{term}, nil, 2, nil, root, nil, PartialModeOff,
		)
	} else {
		files, err = FindFilesWithFirstWordPartial(term, nil, root, nil, PartialModeOff)
	}
	if err != nil {
		t.Fatalf("discover %q: %v", term, err)
	}

	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, filepath.Base(file))
	}
	sort.Strings(names)
	return names
}
