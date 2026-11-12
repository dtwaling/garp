package search

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"garp/config"
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

func TestDiscoveryAgreesWithMatcher(t *testing.T) {
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
		{name: "Unicode term", term: "CAFÉ", text: "café service"},
		{name: "long s fold", term: "service", text: "ſervice"},
		{name: "Kelvin fold", term: "kernel", text: "Kernel"},
		{name: "wildcard", term: "ms-*-api", text: "ms-option-api"},
		{name: "invalid wildcard", term: "a*", text: " a* "},
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
				t.Fatalf("discovery index = %d, matcher index = %d", got, want)
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

func TestFindFilesInlineWildcard(t *testing.T) {
	for _, progress := range []bool{false, true} {
		t.Run(map[bool]string{false: "serial", true: "progress"}[progress], func(t *testing.T) {
			testInlineWildcardDiscovery(t, progress)
		})
	}
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

func TestFindFilesWildcardChunkBoundary(t *testing.T) {
	for _, progress := range []bool{false, true} {
		t.Run(map[bool]string{false: "serial", true: "progress"}[progress], func(t *testing.T) {
			testWildcardChunkBoundaryDiscovery(t, progress)
		})
	}
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

func TestFindFilesPlainChunkBoundary(t *testing.T) {
	for _, progress := range []bool{false, true} {
		t.Run(map[bool]string{false: "serial", true: "progress"}[progress], func(t *testing.T) {
			testPlainChunkBoundaryDiscovery(t, progress)
		})
	}
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

func TestFindFilesRejectsArtificialChunkBoundary(t *testing.T) {
	for _, progress := range []bool{false, true} {
		t.Run(map[bool]string{false: "serial", true: "progress"}[progress], func(t *testing.T) {
			testRejectsArtificialChunkBoundary(t, progress)
		})
	}
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

func TestFileTypeMatcherExtAndName(t *testing.T) {
	// Simulate the --code glob set, which now includes Dockerfile name globs.
	m := newFileTypeMatcher(config.BuildRipgrepFileTypes(true))

	allow := []string{
		"main.go", "service.php", "Unit1.pas", // extensions
		"Dockerfile", "Dockerfile.dev", "Containerfile", "app.Dockerfile", // names
		`C:\repo\Dockerfile`, "/srv/app/Dockerfile.ci", // full paths
	}
	for _, p := range allow {
		if !m.allows(p) {
			t.Errorf("allows(%q) = false, want true", p)
		}
	}

	deny := []string{"README", "notes.unknownext", "image.png", "Dockerfilenotes.bin"}
	for _, p := range deny {
		if m.allows(p) {
			t.Errorf("allows(%q) = true, want false", p)
		}
	}
}

func TestFileTypeMatcherDocsExcludeDockerfile(t *testing.T) {
	// Without --code, Dockerfiles are out of scope.
	m := newFileTypeMatcher(config.BuildRipgrepFileTypes(false))
	if m.allows("Dockerfile") {
		t.Errorf("docs-only matcher should not allow Dockerfile")
	}
	if !m.allows("notes.md") {
		t.Errorf("docs-only matcher should allow notes.md")
	}
}

func TestFileTypeMatcherEmptyAllowsAll(t *testing.T) {
	m := newFileTypeMatcher(nil)
	if !m.allows("anything.xyz") {
		t.Errorf("empty matcher should allow everything")
	}
}

func TestBinaryContainerOver100MiBExtractedViaReaderAtWhenMaxFileSizeOmitted(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.docx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	padding, err := zw.CreateHeader(&zip.FileHeader{Name: "padding", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	// Stream padding to disk without allocating a container-sized buffer.
	chunk := make([]byte, 1024*1024)
	for i := 0; i < 105; i++ {
		if _, err := padding.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	const text = "alpha beta clean document match"
	if _, err := io.WriteString(doc, "<w:document><w:p><w:t>"+text+"</w:t></w:p></w:document>"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	stat, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	const oldCap = 100 * 1024 * 1024
	zr, err := zip.NewReader(file, stat.Size())
	if err != nil {
		t.Fatal(err)
	}
	offset, err := zr.File[1].DataOffset()
	if err != nil || offset <= oldCap || stat.Size() <= oldCap {
		t.Fatalf("fixture XML offset = %d, size = %d, err = %v", offset, stat.Size(), err)
	}
	tail := make([]byte, 4096)
	if _, err := file.ReadAt(tail, stat.Size()-int64(len(tail))); err != nil {
		t.Fatal(err)
	}
	central := bytes.Index(tail, []byte("PK\x01\x02"))
	if central < 0 || stat.Size()-int64(len(tail))+int64(central) <= oldCap {
		t.Fatal("fixture central directory must be past 100 MiB")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, words := range [][]string{{"alpha"}, {"alpha", "beta"}} {
		t.Run(strings.Join(words, "-"), func(t *testing.T) {
			engine := NewSearchEngine(words, []string{"forbidden"}, nil, false, 1, 1000)
			engine.StartDir = root
			engine.Silent = true
			if engine.MaxFileSize != 0 {
				t.Fatal("omitted max-filesize must default to zero")
			}
			results, err := engine.Execute()
			if err != nil || len(results) != 1 {
				t.Fatalf("end-to-end results = %+v, err = %v", results, err)
			}
			if results[0].FileSize != stat.Size() || results[0].CleanContent != text || !strings.Contains(strings.Join(results[0].RawExcerpts, " "), text) {
				t.Fatalf("incorrect size, content or excerpt: %+v", results[0])
			}
			matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
			if err != nil || len(matches) != 1 {
				t.Fatalf("Stage 2 matches = %+v, err = %v", matches, err)
			}
			engine.MaxFileSize = 10 * 1024 * 1024
			results, err = engine.Execute()
			if err != nil || len(results) != 0 {
				t.Fatalf("explicit-limit results = %+v, err = %v", results, err)
			}
			// Bypass admission to verify both extraction stages recheck the limit.
			limited, err := engine.FilterCandidates([]string{path}, 1, time.Now())
			if err != nil || len(limited) != 0 {
				t.Fatalf("explicit-limit Stage 2 matches = %+v, err = %v", limited, err)
			}
			outcome, err := engine.extractAndBuildResultsDetailed(compileQuery(words, PartialModeOff), matches)
			if err != nil || outcome.Matches != 0 || len(outcome.Results) != 0 {
				t.Fatalf("explicit-limit Stage 3 outcome = %+v, err = %v", outcome, err)
			}
		})
	}
}

func TestBinaryFileOver10MiBNotTruncatedWithinMaxFileSize(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	padding, err := zw.CreateHeader(&zip.FileHeader{Name: "padding", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(padding, strings.NewReader(strings.Repeat(" ", 11*1024*1024))); err != nil {
		t.Fatal(err)
	}
	doc, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	const text = "alpha beta clean document match"
	if _, err := io.WriteString(doc, "<w:document><w:p><w:t>"+text+"</w:t></w:p></w:document>"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if bytes.Index(data, []byte("word/document.xml")) <= 10*1024*1024 || bytes.Index(data, []byte("PK\x01\x02")) <= 10*1024*1024 {
		t.Fatal("fixture XML and central directory must be past 10 MiB")
	}
	path := filepath.Join(t.TempDir(), "large.docx")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, words := range [][]string{{"alpha"}, {"alpha", "beta"}} {
		t.Run(strings.Join(words, "-"), func(t *testing.T) {
			engine := NewSearchEngine(words, []string{"forbidden"}, nil, false, 1, 1000)
			engine.MaxFileSize = 25 * 1024 * 1024
			engine.Silent = true
			matches, err := engine.FilterCandidates([]string{path}, 1, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 1 {
				t.Fatalf("Stage 2 matches = %d, want 1", len(matches))
			}
			results, err := engine.ExtractAndBuildResults(matches)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 {
				t.Fatalf("Stage 3 results = %d, want 1", len(results))
			}
			if results[0].FileSize != int64(len(data)) || results[0].CleanContent != text {
				t.Fatalf("incorrect size or extracted text: %+v", results[0])
			}
			if got := strings.Join(results[0].RawExcerpts, " "); !strings.Contains(got, text) || strings.Contains(got, "PK") {
				t.Fatalf("expected clean excerpt, got %q", got)
			}
		})
	}
}

var binarySizePolicyExtensions = []string{
	".pdf", ".doc", ".docx", ".odt", ".rtf", ".eml", ".mbox", ".msg",
}

func writeSizedDiscoveryFixture(t *testing.T, root, name, content string, size int64) {
	t.Helper()
	filename := filepath.Join(root, name)
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Truncate(filename, size); err != nil {
		t.Fatalf("resize %s: %v", name, err)
	}
}

func discoveryBasenames(paths []string) []string {
	names := make([]string, len(paths))
	for i, filename := range paths {
		names[i] = filepath.Base(filename)
	}
	sort.Strings(names)
	return names
}

func assertStage1Discovery(
	t *testing.T,
	root string,
	options WalkOptions,
	want []string,
) {
	t.Helper()
	sort.Strings(want)

	plain, err := FindFilesWithFirstWordPartialWithOptions(
		"anchor", nil, root, nil, PartialModeOff, options,
	)
	if err != nil {
		t.Fatalf("plain Stage 1 discovery: %v", err)
	}
	if got := discoveryBasenames(plain); !reflect.DeepEqual(got, want) {
		t.Errorf("plain Stage 1 candidates = %v, want %v", got, want)
	}

	progress, err := FindFilesWithFirstWordProgressPartialWithOptions(
		[]string{"anchor"}, nil, 2, nil, root, nil, PartialModeOff, options,
	)
	if err != nil {
		t.Fatalf("progress Stage 1 discovery: %v", err)
	}
	if got := discoveryBasenames(progress); !reflect.DeepEqual(got, want) {
		t.Errorf("progress Stage 1 candidates = %v, want %v", got, want)
	}
}

func TestBinarySizePolicyDefaultAndExplicitLimits(t *testing.T) {
	root := t.TempDir()
	writeSizedDiscoveryFixture(t, root, "text-at-limit.txt", "anchor", defaultTextMaxBytes)
	writeSizedDiscoveryFixture(t, root, "text-over-limit.txt", "anchor", defaultTextMaxBytes+1)
	for _, ext := range binarySizePolicyExtensions {
		name := "large" + ext
		if !IsBinaryFormat(name) {
			t.Fatalf("IsBinaryFormat(%q) = false", name)
		}
		writeSizedDiscoveryFixture(t, root, name, "anchor", defaultTextMaxBytes+1)
	}

	count, err := GetDocumentFileCountWithOptions(nil, root, nil, WalkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 + len(binarySizePolicyExtensions); count != want {
		t.Errorf("default candidate count = %d, want %d", count, want)
	}

	count, err = GetDocumentFileCountWithOptions(
		nil, root, nil, WalkOptions{MaxFileSize: defaultTextMaxBytes},
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("explicit-limit candidate count = %d, want 1", count)
	}
}

func TestStage1DefaultMaxFileSizeBoundaries(t *testing.T) {
	root := t.TempDir()
	writeSizedDiscoveryFixture(t, root, "matched-at-limit.txt", "anchor", defaultTextMaxBytes)
	writeSizedDiscoveryFixture(t, root, "unmatched-at-limit.txt", "absent", defaultTextMaxBytes)
	writeSizedDiscoveryFixture(t, root, "matched-over-limit.txt", "anchor", defaultTextMaxBytes+1)

	want := []string{"matched-at-limit.txt"}
	for _, ext := range binarySizePolicyExtensions {
		name := "large" + ext
		writeSizedDiscoveryFixture(t, root, name, "anchor", defaultTextMaxBytes+1)
		want = append(want, name)
	}

	assertStage1Discovery(t, root, WalkOptions{}, want)
}

func TestStage1ExplicitMaxFileSizeRejectsEveryFormat(t *testing.T) {
	const limit int64 = 32
	root := t.TempDir()
	writeSizedDiscoveryFixture(t, root, "small.txt", "anchor", limit)
	writeSizedDiscoveryFixture(t, root, "large.txt", "anchor", limit+1)
	for _, ext := range binarySizePolicyExtensions {
		writeSizedDiscoveryFixture(t, root, "large"+ext, "anchor", limit+1)
	}

	assertStage1Discovery(t, root, WalkOptions{MaxFileSize: limit}, []string{"small.txt"})
}

func TestStage2ReadCap(t *testing.T) {
	const limit int64 = 96
	root := t.TempDir()

	t.Run("streaming terms after cap", func(t *testing.T) {
		filename := filepath.Join(root, "stream-post-cap.txt")
		content := strings.Repeat(" ", int(limit)) + "alpha beta"
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		found, decided := streamContainsAllWordsDecidedPartial(
			filename, []string{"alpha", "beta"}, PartialModeOff, limit,
		)
		if found || !decided {
			t.Fatalf("all-words result = (%v, %v), want (false, true)", found, decided)
		}

		found, decided = streamContainsRankedWordsDecidedPartial(
			filename, []string{"alpha", "beta"}, true, PartialModeOff, limit,
		)
		if found || !decided {
			t.Fatalf("ranked result = (%v, %v), want (false, true)", found, decided)
		}
	})

	t.Run("distance checks ignore post-cap cluster", func(t *testing.T) {
		filename := filepath.Join(root, "distance-post-cap.txt")
		prefix := "alpha " + strings.Repeat("pad ", 18) + "beta"
		prefix += strings.Repeat(" ", int(limit)-len(prefix))
		if err := os.WriteFile(filename, []byte(prefix+" alpha beta"), 0o600); err != nil {
			t.Fatal(err)
		}

		ok, err := checkFileContainsAllWordsPartial(
			filename, []string{"alpha", "beta"}, 16, true, PartialModeOff, limit,
		)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("all-words distance check matched a cluster after the cap")
		}

		_, _, _, _, ok, err = checkFileContainsRankedWordsPartial(
			filename, []string{"alpha", "beta"}, 16, true, true, PartialModeOff, limit,
		)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("ranked distance check matched a cluster after the cap")
		}
	})

	t.Run("exclusion after cap", func(t *testing.T) {
		filename := filepath.Join(root, "exclude-post-cap.txt")
		content := strings.Repeat(" ", int(limit)) + "forbidden"
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		searchText, err := readStage2SearchText(filename, limit)
		if err != nil {
			t.Fatal(err)
		}
		if CheckTextContainsExcludeWords(searchText, []string{"forbidden"}) {
			t.Fatal("exclusion check matched a term after the cap")
		}
	})
}

func TestStage2FileGrowthUsesReadCap(t *testing.T) {
	const limit int64 = 64
	root := t.TempDir()
	filename := filepath.Join(root, "growing.txt")
	prefix := "alpha" + strings.Repeat(" ", int(limit)-len("alpha"))
	if err := os.WriteFile(filename, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}

	candidates, err := FindFilesWithFirstWordPartialWithOptions(
		"alpha", nil, root, nil, PartialModeOff, WalkOptions{MaxFileSize: limit},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discovery candidates = %v, want one", candidates)
	}
	if err := os.WriteFile(filename, []byte(prefix+" beta forbidden"), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := NewSearchEngine([]string{"alpha", "beta"}, nil, nil, false, 1, 1000)
	engine.MaxFileSize = limit
	engine.Silent = true
	matches, err := engine.FilterCandidates(candidates, len(candidates), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("grown file matched terms after cap: %+v", matches)
	}

	engine = NewSearchEngine([]string{"alpha"}, []string{"forbidden"}, nil, false, 1, 1000)
	engine.MaxFileSize = limit
	engine.Silent = true
	matches, err = engine.FilterCandidates(candidates, len(candidates), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("grown file excluded by term after cap; matches = %+v", matches)
	}
}

func TestBuildTermRegexLowerModes(t *testing.T) {
	tests := []struct {
		name    string
		word    string
		mode    PartialMode
		matches []string
		rejects []string
	}{
		{
			name:    "off preserves legacy whole word inflections",
			word:    "deploy",
			mode:    PartialModeOff,
			matches: []string{"deploy", "deploys", "deployes"},
			rejects: []string{"deployed", "deployment", "redeploy"},
		},
		{
			name:    "prefix expands suffixes without internal substrings",
			word:    "deploy",
			mode:    PartialModePrefix,
			matches: []string{"deploy", "deployed", "deployment", "deploy_hook"},
			rejects: []string{"redeploy", "autodeploy"},
		},
		{
			name:    "contains matches raw substrings",
			word:    "deploy",
			mode:    PartialModeContains,
			matches: []string{"deploy", "deployed", "redeploy", "autodeploy"},
		},
		{
			name:    "short term prefix falls back to whole word",
			word:    "in",
			mode:    PartialModePrefix,
			matches: []string{"in", "ins"},
			rejects: []string{"inside"},
		},
		{
			name:    "short term contains falls back to whole word",
			word:    "in",
			mode:    PartialModeContains,
			matches: []string{"in", "ins"},
			rejects: []string{"inside"},
		},
		{
			name:    "three character contains remains partial",
			word:    "cat",
			mode:    PartialModeContains,
			matches: []string{"application"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re := buildTermRegexLower(tt.word, tt.mode)
			for _, text := range tt.matches {
				if !re.MatchString(text) {
					t.Errorf("%q did not match %q", re.String(), text)
				}
			}
			for _, text := range tt.rejects {
				if re.MatchString(text) {
					t.Errorf("%q unexpectedly matched %q", re.String(), text)
				}
			}
		})
	}
}

func TestBuildTermRegexCapturesTokenWithoutBoundary(t *testing.T) {
	for _, text := range []string{"_deploy_hook", ".deploy_hook"} {
		re := buildTermRegexLower("deploy", PartialModePrefix)
		indexes := re.FindStringSubmatchIndex(text)
		if len(indexes) < 4 || indexes[2] < 0 {
			t.Fatalf("%q did not return capture group 1 indexes: %v", text, indexes)
		}
		if got := text[indexes[2]:indexes[3]]; got != "deploy_hook" {
			t.Errorf("group 1 = %q, want deploy_hook", got)
		}
		if indexes[2] != 1 {
			t.Errorf("group 1 started at %d, want 1", indexes[2])
		}
	}
}

func TestBuildTermRegexCIIsCaseInsensitive(t *testing.T) {
	for _, word := range []string{"Deploy", "DEPLOY", "deploy"} {
		re := buildTermRegexCI(word, PartialModePrefix)
		for _, text := range []string{"deploy", "DEPLOYMENT", "Deploy_Hook"} {
			if !re.MatchString(text) {
				t.Errorf("buildTermRegexCI(%q) %q did not match %q", word, re.String(), text)
			}
		}
	}
}

func TestBuildTermRegexPrefixRejectsInternalSubstring(t *testing.T) {
	re := buildTermRegexLower("cat", PartialModePrefix)
	if re.MatchString("application") {
		t.Errorf("%q unexpectedly matched internal cat in application", re.String())
	}
	if !strings.Contains(re.String(), "(") {
		t.Errorf("%q does not contain a capture group", re.String())
	}
}

func TestInlineGlob(t *testing.T) {
	tests := []struct {
		name    string
		word    string
		mode    PartialMode
		matches []string
		rejects []string
	}{
		{
			name:    "glob expands prefix while off",
			word:    "deploy*",
			mode:    PartialModeOff,
			matches: []string{"deploy", "deployment", "deployed"},
			rejects: []string{"redeploy"},
		},
		{
			name:    "glob is idempotent with prefix mode",
			word:    "deploy*",
			mode:    PartialModePrefix,
			matches: []string{"deploy", "deployment", "deployed"},
			rejects: []string{"redeploy"},
		},
		{
			name:    "glob cleans suffix in contains mode",
			word:    "deploy*",
			mode:    PartialModeContains,
			matches: []string{"deploy", "redeploy"},
		},
		{
			name:    "one character glob stays literal",
			word:    "a*",
			mode:    PartialModeOff,
			rejects: []string{"a", "apple"},
		},
		{
			name:    "two character glob expands prefix",
			word:    "in*",
			mode:    PartialModeOff,
			matches: []string{"inside"},
			rejects: []string{"begin"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re := buildTermRegexLower(tt.word, tt.mode)
			for _, text := range tt.matches {
				if !re.MatchString(text) {
					t.Errorf("%q did not match %q", re.String(), text)
				}
			}
			for _, text := range tt.rejects {
				if re.MatchString(text) {
					t.Errorf("%q unexpectedly matched %q", re.String(), text)
				}
			}
		})
	}

	ci := buildTermRegexCI("Deploy*", PartialModeOff)
	if !ci.MatchString("DEPLOYMENT") {
		t.Errorf("case-insensitive glob %q did not match DEPLOYMENT", ci.String())
	}

	glob := buildTermRegexLower("deploy*", PartialModeOff)
	strict := buildTermRegexLower("service", PartialModeOff)
	if !glob.MatchString("deployment") {
		t.Error("deploy* did not match deployment in a mixed query")
	}
	if strict.MatchString("services_handler") {
		t.Error("unmarked service matched services_handler in a mixed query")
	}
}

func TestPathScopePartsMatch(t *testing.T) {
	for _, tt := range []struct {
		pattern, candidate string
		want               bool
	}{
		{"", "", true},
		{"", "a", false},
		{"a", "", false},
		{"**/**", "", true},
		{"a/**/**/b", "a/b", true},
		{"a/**/**/b", "a/x/y/b", true},
		{"a/**/**/b", "a/x/y/c", false},
		{"**/a/**/b", "x/a/y/a/b", true},
		{"a/**", "a", true},
		{"a", "a/b", false},
		{"*/b", "a/x/b", false},
		{"**/[ab]?.go", "src/a1.go", true},
		{"**/[ab]?.go", "src/c1.go", false},
		{"**/[", "a/b", false},
		{"A/**", "a/b", false},
		{`\*\*/x`, "**/x", true},
		{`\*\*/x`, "**/**/x", false},
	} {
		t.Run(tt.pattern+":"+tt.candidate, func(t *testing.T) {
			pattern := splitPathScopeParts(tt.pattern)
			candidate := splitPathScopeParts(tt.candidate)
			beforePattern := append([]string(nil), pattern...)
			beforeCandidate := append([]string(nil), candidate...)
			if got := pathScopePartsMatch(pattern, candidate); got != tt.want {
				t.Fatalf("match(%q, %q) = %v, want %v", tt.pattern, tt.candidate, got, tt.want)
			}
			if !reflect.DeepEqual(pattern, beforePattern) || !reflect.DeepEqual(candidate, beforeCandidate) {
				t.Fatal("matching mutated input parts")
			}
		})
	}
}

// Keep the exhaustive oracle small: it deliberately uses the original recursive
// semantics to check all combinations of short patterns and literal path parts.
func TestPathScopePartsMatchExhaustive(t *testing.T) {
	var reference func([]string, []string) bool
	reference = func(pattern, candidate []string) bool {
		if len(pattern) == 0 {
			return len(candidate) == 0
		}
		if pattern[0] == "**" {
			for i := 0; i <= len(candidate); i++ {
				if reference(pattern[1:], candidate[i:]) {
					return true
				}
			}
			return false
		}
		if len(candidate) == 0 {
			return false
		}
		matched, err := path.Match(pattern[0], candidate[0])
		return err == nil && matched && reference(pattern[1:], candidate[1:])
	}
	sequences := func(alphabet []string, depth int) [][]string {
		result := [][]string{nil}
		start := 0
		for i := 0; i < depth; i++ {
			end := len(result)
			for _, prefix := range result[start:end] {
				for _, part := range alphabet {
					next := append([]string(nil), prefix...)
					result = append(result, append(next, part))
				}
			}
			start = end
		}
		return result
	}
	patterns := sequences([]string{"a", "b", "*", "**", "?", "[ab]", "["}, 4)
	candidates := sequences([]string{"a", "b", "**"}, 3)
	for _, pattern := range patterns {
		for _, candidate := range candidates {
			want := reference(pattern, candidate)
			if got := pathScopePartsMatch(pattern, candidate); got != want {
				t.Fatalf("match(%q, %q) = %v, want %v", pattern, candidate, got, want)
			}
		}
	}
	t.Logf("verified %d pattern/path combinations", len(patterns)*len(candidates))
}

func TestCheckTextContainsRankedWords(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		words     []string
		distance  int
		strict    bool
		wantScore int
		wantCount int
		wantTerms []string
		wantSpan  int
		wantOK    bool
	}{
		{
			name:      "one term matches",
			text:      "alpha",
			words:     []string{"alpha"},
			distance:  20,
			wantScore: 5,
			wantCount: 1,
			wantTerms: []string{"alpha"},
			wantSpan:  5,
			wantOK:    true,
		},
		{
			name:     "one term missing",
			text:     "beta",
			words:    []string{"alpha"},
			distance: 20,
			wantOK:   false,
		},
		{
			name:      "two terms require conjunction within distance",
			text:      "alpha beta",
			words:     []string{"alpha", "beta"},
			distance:  20,
			wantScore: 9,
			wantCount: 2,
			wantTerms: []string{"alpha", "beta"},
			wantSpan:  10,
			wantOK:    true,
		},
		{
			name:     "two terms outside distance fail",
			text:     "alpha " + strings.Repeat("x", 30) + " beta",
			words:    []string{"alpha", "beta"},
			distance: 20,
			wantOK:   false,
		},
		{
			name:     "two terms with one missing fail",
			text:     "alpha",
			words:    []string{"alpha", "beta"},
			distance: 20,
			wantOK:   false,
		},
		{
			name:      "relaxed three terms accepts primary and secondary",
			text:      "alpha beta",
			words:     []string{"alpha", "beta", "synchronization"},
			distance:  20,
			wantScore: 9,
			wantCount: 2,
			wantTerms: []string{"alpha", "beta"},
			wantSpan:  10,
			wantOK:    true,
		},
		{
			name:      "relaxed three terms favors long secondary",
			text:      "alpha synchronization",
			words:     []string{"alpha", "beta", "synchronization"},
			distance:  30,
			wantScore: 20,
			wantCount: 2,
			wantTerms: []string{"alpha", "synchronization"},
			wantSpan:  21,
			wantOK:    true,
		},
		{
			name:     "relaxed three terms rejects missing primary",
			text:     "beta synchronization",
			words:    []string{"alpha", "beta", "synchronization"},
			distance: 30,
			wantOK:   false,
		},
		{
			name:     "relaxed three terms rejects primary alone",
			text:     "alpha",
			words:    []string{"alpha", "beta", "synchronization"},
			distance: 30,
			wantOK:   false,
		},
		{
			name:      "relaxed three terms accepts all terms",
			text:      "alpha beta synchronization",
			words:     []string{"alpha", "beta", "synchronization"},
			distance:  30,
			wantScore: 24,
			wantCount: 3,
			wantTerms: []string{"alpha", "beta", "synchronization"},
			wantSpan:  26,
			wantOK:    true,
		},
		{
			name:     "strict mode requires every term",
			text:     "alpha beta",
			words:    []string{"alpha", "beta", "synchronization"},
			distance: 30,
			strict:   true,
			wantOK:   false,
		},
		{
			name:      "distinct terms are not inflated by repeats",
			text:      "alpha beta beta beta",
			words:     []string{"alpha", "beta", "synchronization"},
			distance:  30,
			wantScore: 9,
			wantCount: 2,
			wantTerms: []string{"alpha", "beta"},
			wantSpan:  10,
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, count, terms, span, ok := CheckTextContainsRankedWords(tt.text, tt.words, tt.distance, tt.strict)
			if score != tt.wantScore || count != tt.wantCount || span != tt.wantSpan || ok != tt.wantOK {
				t.Fatalf("CheckTextContainsRankedWords() = (%d, %d, %v, %d, %v), want (%d, %d, %v, %d, %v)", score, count, terms, span, ok, tt.wantScore, tt.wantCount, tt.wantTerms, tt.wantSpan, tt.wantOK)
			}
			if strings.Join(terms, "\x00") != strings.Join(tt.wantTerms, "\x00") {
				t.Fatalf("matched terms = %v, want %v", terms, tt.wantTerms)
			}
		})
	}
}

func TestCheckTextContainsRankedWordsChoosesBestWindow(t *testing.T) {
	t.Run("rarity weighting beats a denser short-term cluster", func(t *testing.T) {
		text := "term go to " + strings.Repeat("x", 40) + " term synchronization"
		score, count, terms, span, ok := CheckTextContainsRankedWords(text, []string{"term", "synchronization", "go", "to"}, 30, false)
		if !ok {
			t.Fatal("expected a valid ranked match")
		}
		if score != 19 || count != 2 || strings.Join(terms, ",") != "term,synchronization" || span != 20 {
			t.Fatalf("best window = (%d, %d, %v, %d), want (19, 2, [term synchronization], 20)", score, count, terms, span)
		}
	})

	t.Run("exhaustive scan favors later higher scoring cluster", func(t *testing.T) {
		text := "term " + strings.Repeat("x", 190) + " go " + strings.Repeat("y", 210) + " term synchronization beta"
		score, count, terms, span, ok := CheckTextContainsRankedWords(text, []string{"term", "synchronization", "beta", "go"}, 200, false)
		if !ok {
			t.Fatal("expected a valid ranked match")
		}
		if score != 23 || count != 3 || strings.Join(terms, ",") != "term,synchronization,beta" || span != 25 {
			t.Fatalf("best window = (%d, %d, %v, %d), want (23, 3, [term synchronization beta], 25)", score, count, terms, span)
		}
	})

	t.Run("equal score and term count favor tighter span", func(t *testing.T) {
		text := "alpha " + strings.Repeat("x", 90) + " beta alpha beta"
		score, count, terms, span, ok := CheckTextContainsRankedWords(text, []string{"alpha", "beta"}, 200, false)
		if !ok {
			t.Fatal("expected a valid ranked match")
		}
		if score != 9 || count != 2 || strings.Join(terms, ",") != "alpha,beta" || span != 10 {
			t.Fatalf("best window = (%d, %d, %v, %d), want (9, 2, [alpha beta], 10)", score, count, terms, span)
		}
	})
}

func TestCheckTextContainsAllWordsDelegatesToStrictRankedMatching(t *testing.T) {
	if !CheckTextContainsAllWords("alpha beta", []string{"alpha", "beta"}, 20) {
		t.Fatal("strict compatibility wrapper rejected a valid two-term match")
	}
	if CheckTextContainsAllWords("alpha beta", []string{"alpha", "beta", "gamma"}, 20) {
		t.Fatal("strict compatibility wrapper accepted a partial three-term match")
	}
}

// buildTestTree creates a temporary directory tree for walk tests.
// Structure:
//
//	root/
//	  backend/
//	    service/
//	      auth.go
//	      auth.md
//	    Assembly/
//	      main.cs
//	      main.md
//	  frontend/
//	    app.ts
//	    app.md
//	  tests/
//	    backend_test.go
//	    frontend_test.go
//	    endpoint_test.md
func buildTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	files := map[string]string{
		"backend/service/auth.go":                       "package service\nfunc Auth() {}\n",
		"backend/service/auth.md":                       "# auth\ntoken authentication service\n",
		"backend/Assembly/main.cs":                      "using System;\nclass Main { static void Main() {} }\n",
		"backend/Assembly/main.md":                      "# Assembly main\nassembly entry point\n",
		"app/Http/Controllers/Auth/OAuthController.php": "provider access_token controller\n",
		"app/Http/Requests/LoginRequest.php":            "provider access_token request\n",
		"frontend/app.ts":                               "export function app() {}\n",
		"frontend/app.md":                               "# frontend app\napp description\n",
		"tests/backend_test.go":                         "package tests\nfunc TestBackend() {}\n",
		"tests/frontend_test.go":                        "package tests\nfunc TestFrontend() {}\n",
		"tests/endpoint_test.md":                        "# endpoint test\nendpoint coverage\n",
	}
	for rel, content := range files {
		writeWalkFixture(t, root, rel, content)
	}
	return root
}

// TestFindFilesWithFirstWordProgress_MultiChunkBoundary exercises the streaming
// (>64KiB) candidate-filter path, which previously had no coverage. The needle's
// only occurrence straddles the 64KiB chunk boundary, so finding it requires the
// reader's overlap+multi-read logic (the loop that carried the buf[:toRead] vs
// buf[:n] slicing). A large non-matching file must not be reported.
func TestFindFilesWithFirstWordProgress_MultiChunkBoundary(t *testing.T) {
	root := t.TempDir()
	const chunk = 64 * 1024

	// big.md: a single long token fills chunk 1, a word boundary lands at the
	// 64KiB edge, then "needlematch" straddles it, then trailing filler keeps
	// the file multi-chunk (~73KB).
	var b strings.Builder
	b.Grow(80 * 1024)
	b.WriteString(strings.Repeat("x", chunk-4)) // 65532 bytes: one big token
	b.WriteByte(' ')                            // word boundary at offset 65532
	b.WriteString("needlematch")                // starts at 65533, crosses 65536
	b.WriteByte(' ')
	b.WriteString(strings.Repeat("x", 8*1024)) // trailing filler -> 2+ chunks
	if err := os.WriteFile(filepath.Join(root, "big.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write big.md: %v", err)
	}

	// ctrl.md: large, multi-chunk, no needle anywhere.
	ctrl := strings.Repeat("filler ", 12000) // ~84KB
	if err := os.WriteFile(filepath.Join(root, "ctrl.md"), []byte(ctrl), 0o644); err != nil {
		t.Fatalf("write ctrl.md: %v", err)
	}

	files, err := FindFilesWithFirstWordProgress(
		[]string{"needlematch"}, []string{"-g", "*.md"}, 2, nil, root, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gotBig, gotCtrl bool
	for _, f := range files {
		switch filepath.Base(f) {
		case "big.md":
			gotBig = true
		case "ctrl.md":
			gotCtrl = true
		}
	}
	if !gotBig {
		t.Errorf("needle straddling the 64KiB boundary not found in big.md; matches=%v", files)
	}
	if gotCtrl {
		t.Errorf("control file with no needle was incorrectly reported as a match")
	}
}

// allFileTypes includes both doc and code types for tests
func allFileTypes() []string {
	return []string{
		"-g", "*.md",
		"-g", "*.go",
		"-g", "*.cs",
		"-g", "*.ts",
		"-g", "*.php",
	}
}

func TestDiscoveryWalkRoot(t *testing.T) {
	root := buildTestTree(t)
	for _, tt := range []struct {
		name, subdir, term string
		wantCount          int
	}{
		{name: "count all", wantCount: 11},
		{name: "count backend", subdir: "backend", wantCount: 4},
		{name: "find auth", term: "auth"},
		{name: "find frontend app", subdir: "frontend", term: "app"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			walkRoot := filepath.Join(root, tt.subdir)
			if tt.term == "" {
				count, err := GetDocumentFileCount(allFileTypes(), walkRoot, nil)
				if err != nil {
					t.Fatal(err)
				}
				if count != tt.wantCount {
					t.Errorf("count = %d, want %d", count, tt.wantCount)
				}
				return
			}
			files, err := FindFilesWithFirstWord(tt.term, allFileTypes(), walkRoot, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) == 0 {
				t.Fatalf("expected matches for %q", tt.term)
			}
			for _, file := range files {
				if !fileUnderRoot(file, walkRoot) {
					t.Errorf("result path %q leaks outside root %q", file, walkRoot)
				}
			}
		})
	}
}

func TestGetDocumentFileCount_PathScope(t *testing.T) {
	root := buildTestTree(t)
	for _, tt := range []struct {
		name     string
		scope    []string
		rawScope string
		want     int
	}{
		{name: "BackendAssembly", scope: []string{"*/Assembly/*"}, want: 2},
		{name: "BackendAssemblyWindowsSeparators", scope: []string{`backend\Assembly`}, want: 2},
		{name: "GlobstarController", scope: []string{`app\Http\**\*Controller.php`}, want: 1},
		{name: "LeadingGlobstarController", scope: []string{`**\*Controller.php`}, want: 1},
		{name: "BasenamePatternMatchesAnywhere", scope: []string{`*Controller.php`}, want: 1},
		{name: "GlobstarLiteralPrefix", scope: []string{`app\Http\**\*.php`}, want: 2},
		{name: "QuotedCommaSeparatedGlobstars", rawScope: `'app\Http\**\*.php,app\**\*Controller.php'`, want: 2},
		{name: "AbsoluteDir", scope: []string{filepath.Join(root, "tests")}, want: 3},
		{name: "Tests", scope: []string{"tests/*"}, want: 3},
		{name: "MultiplePatterns", scope: []string{"*/Assembly/*", "tests/*"}, want: 5},
		{name: "NilMeansAll", want: 11},
		{name: "EmptyMeansAll", scope: []string{}, want: 11},
		// Directory-prefix shorthand works with and without a trailing slash.
		{name: "TrailingSlash", scope: []string{"tests/"}, want: 3},
		{name: "BareDir", scope: []string{"tests"}, want: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scope := tt.scope
			if tt.rawScope != "" {
				var err error
				scope, err = ValidatePathScope(tt.rawScope)
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
			}
			count, err := GetDocumentFileCount(allFileTypes(), root, scope)
			if err != nil {
				t.Fatal(err)
			}
			if count != tt.want {
				t.Errorf("scope %q: count = %d, want %d", scope, count, tt.want)
			}
		})
	}
}

func TestGetDocumentFileCount_PathScope_DirWildcardMatchesNestedFiles(t *testing.T) {
	root := buildTestTree(t)
	nestedDir := filepath.Join(root, "backend", "Assembly", "Nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "nested.md"), []byte("# nested assembly\n"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	scope := []string{`backend\Assembly\*`}
	count, err := GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 files under backend\\Assembly\\*, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_QuotedDirWildcardMatchesNestedFiles(t *testing.T) {
	root := buildTestTree(t)
	nestedDir := filepath.Join(root, "app", "Http", "Controllers", "Auth")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "OAuthController.md"), []byte("provider access_token\n"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	scope := []string{`'app\Http\*'`}
	count, err := GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 files under quoted app\\Http\\*, got %d", count)
	}
}

func TestGetDocumentFileCount_PathScope_WindowsCaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows filesystems are matched case-insensitively")
	}

	root := buildTestTree(t)
	scope := []string{`backend\assembly`}
	count, err := GetDocumentFileCount(allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 files matching Windows case-insensitive pathscope, got %d", count)
	}
}

func TestFindFilesWithFirstWord_PathScope_Assembly(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{"*/Assembly/*"}
	// "assembly" appears in backend/Assembly/main.md
	files, err := FindFilesWithFirstWord("assembly", allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected a match for 'assembly' in */Assembly/*")
	}
	for _, f := range files {
		if !fileUnderRoot(f, filepath.Join(root, "backend", "Assembly")) {
			t.Errorf("result %q should be under Assembly/, got it outside scope", f)
		}
	}
}

func TestFindFilesWithFirstWord_PathScope_NoMatch(t *testing.T) {
	root := buildTestTree(t)
	scope := []string{"tests/*"}
	// "assembly" does NOT appear in tests/
	files, err := FindFilesWithFirstWord("assembly", allFileTypes(), root, scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected no results for 'assembly' in tests/ scope, got %v", files)
	}
}

func TestSearchEngine_FindsMatchesUnderBuildDirectory(t *testing.T) {
	root := t.TempDir()
	buildDir := filepath.Join(root, "doc", "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir build dir: %v", err)
	}
	for _, name := range []string{"deployment-process.md", "deployment-plan.md"} {
		content := "The approved parent AMI produces the shared base AMI."
		if err := os.WriteFile(filepath.Join(buildDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	tests := []struct {
		name  string
		scope []string
	}{
		{name: "unscoped"},
		{name: "Windows-style doc scope", scope: []string{`doc\`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewSearchEngineWithWorkers(
				[]string{"ami", "parent", "base"},
				nil,
				[]string{"-g", "*.md"},
				false,
				1,
				1000,
				2,
			)
			engine.StartDir = root
			engine.PathScope = tt.scope
			engine.Distance = 200
			engine.Silent = true

			results, err := engine.Execute()
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if len(results) != 2 {
				t.Fatalf("Execute() returned %d build-directory matches, want 2", len(results))
			}
		})
	}
}

// fileUnderRoot returns true if path is under (or equal to) root.
func fileUnderRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	// Relative path must not start with ".."
	return len(rel) > 0 && rel[0] != '.'
}
