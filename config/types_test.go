package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCodeTypesIncludeDelphiAndPHP(t *testing.T) {
	want := []string{
		"php", "phtml", // PHP source + templates
		"pas", "dpr", "dpk", "inc", "dfm", "fmx", "dproj", "groupproj", // Delphi
	}
	for _, ext := range want {
		if !slices.Contains(CodeTypes, ext) {
			t.Errorf("CodeTypes missing %q", ext)
		}
	}
}

func TestIsCodeFileDelphi(t *testing.T) {
	cases := []string{"Unit1.pas", "Project1.dpr", "MyPkg.dpk", "shared.inc",
		"MainForm.dfm", "MobileForm.fmx", "App.dproj", "Suite.groupproj", "view.phtml"}
	for _, name := range cases {
		if !IsCodeFile(name) {
			t.Errorf("IsCodeFile(%q) = false, want true", name)
		}
	}
}

func TestBuildRipgrepFileTypesCarriesCodeRegistryGlobs(t *testing.T) {
	got := globSet(BuildRipgrepFileTypes(true))
	want := make(map[string]bool, len(CodeTypes)+len(CodeFilenames))
	for _, ext := range CodeTypes {
		glob := "*." + ext
		want[glob] = true
		if !got[glob] {
			t.Errorf("BuildRipgrepFileTypes(true) missing CodeTypes glob %q", glob)
		}
	}
	for _, name := range CodeFilenames {
		want[name] = true
		if !got[name] {
			t.Errorf("BuildRipgrepFileTypes(true) missing CodeFilenames glob %q", name)
		}
	}
	// Without --code, code globs must NOT appear.
	docsOnly := globSet(BuildRipgrepFileTypes(false))
	for glob := range got {
		if !docsOnly[glob] && !want[glob] {
			t.Errorf("BuildRipgrepFileTypes(true) has unregistered code glob %q", glob)
		}
	}
}

func TestBuildRipgrepFileTypesHas28DefaultDocumentExtensions(t *testing.T) {
	globs := BuildRipgrepFileTypes(false)
	if got := len(globs) / 2; got != 28 {
		t.Fatalf("default document glob count = %d, want 28", got)
	}
	for i := 0; i < len(globs); i += 2 {
		if globs[i] != "-g" || !strings.HasPrefix(globs[i+1], "*.") {
			t.Fatalf("default document glob pair %d = %q %q", i/2, globs[i], globs[i+1])
		}
	}
}

func TestCodeRegistryIncludesOnlySafeEnvironmentTemplates(t *testing.T) {
	codeGlobs := globSet(BuildRipgrepFileTypes(true))
	for _, name := range []string{".env.example", ".env.sample", ".env.template"} {
		if !codeGlobs[name] {
			t.Errorf("BuildRipgrepFileTypes(true) missing safe environment template %q", name)
		}
		if !IsCodeFile(name) {
			t.Errorf("IsCodeFile(%q) = false, want true", name)
		}
	}
	for _, name := range []string{".env", ".env.local", ".env.development", ".env.production"} {
		if matchesAnyGlob(codeGlobs, name) {
			t.Errorf("BuildRipgrepFileTypes(true) matches secret environment file %q", name)
		}
		if IsCodeFile(name) {
			t.Errorf("IsCodeFile(%q) = true, want false", name)
		}
	}
}

func TestGetFileTypeDescriptionCarriesCodeRegistry(t *testing.T) {
	got := GetFileTypeDescription(true)
	for _, ext := range CodeTypes {
		if !strings.Contains(got, ext) {
			t.Errorf("GetFileTypeDescription(true) missing CodeTypes entry %q", ext)
		}
	}
	for _, name := range CodeFilenames {
		if !strings.Contains(got, name) {
			t.Errorf("GetFileTypeDescription(true) missing CodeFilenames entry %q", name)
		}
	}
}

func globSet(args []string) map[string]bool {
	globs := make(map[string]bool)
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == "-g" {
			globs[args[i+1]] = true
		}
	}
	return globs
}

func matchesAnyGlob(globs map[string]bool, name string) bool {
	for glob := range globs {
		if matched, err := filepath.Match(strings.ToLower(glob), strings.ToLower(name)); err == nil && matched {
			return true
		}
	}
	return false
}

func TestIsCodeFileDockerfile(t *testing.T) {
	codeNames := []string{
		"Dockerfile", "dockerfile", "DOCKERFILE", // case-insensitive
		"Dockerfile.dev", "Dockerfile.prod", // variant suffixes
		"Containerfile",                    // Podman
		"app.Dockerfile", "web.dockerfile", // extension-bearing
		`C:\repo\build\Dockerfile`, "/srv/app/Dockerfile.ci", // full paths
	}
	for _, name := range codeNames {
		if !IsCodeFile(name) {
			t.Errorf("IsCodeFile(%q) = false, want true", name)
		}
	}
	// Must NOT match unrelated files that merely contain the word.
	for _, name := range []string{"Dockerfileutil.go", "MyDockerfileNotes.txt", "notes.md"} {
		if IsCodeFilename(name) {
			t.Errorf("IsCodeFilename(%q) = true, want false", name)
		}
	}
}

func TestBuildRipgrepFileTypesCarriesDockerfileGlobs(t *testing.T) {
	got := strings.Join(BuildRipgrepFileTypes(true), " ")
	for _, g := range []string{"Dockerfile", "Dockerfile.*", "Containerfile", "*.dockerfile"} {
		if !strings.Contains(got, g) {
			t.Errorf("BuildRipgrepFileTypes(true) missing Dockerfile glob %q", g)
		}
	}
	// Name globs must NOT leak into the documents-only set.
	if strings.Contains(strings.Join(BuildRipgrepFileTypes(false), " "), "Containerfile") {
		t.Errorf("BuildRipgrepFileTypes(false) should not include Dockerfile globs")
	}
}

func TestOnlyTypeGlobsDockerfile(t *testing.T) {
	got := strings.Join(OnlyTypeGlobs("dockerfile"), " ")
	for _, g := range []string{"Dockerfile", "Dockerfile.*", "Containerfile", "*.dockerfile"} {
		if !strings.Contains(got, g) {
			t.Errorf("OnlyTypeGlobs(dockerfile) missing %q; got %q", g, got)
		}
	}
	// A normal type stays a single extension glob.
	if got := strings.Join(OnlyTypeGlobs("pas"), " "); got != "-g *.pas" {
		t.Errorf("OnlyTypeGlobs(pas) = %q, want \"-g *.pas\"", got)
	}
}

func TestOnlyTypeGlobsOrderedUnion(t *testing.T) {
	got := OnlyTypesGlobs([]string{"md", "dockerfile", "MD", "txt", "dockerfile"})
	want := append([]string{"-g", "*.md"}, DockerfileGlobs()...)
	want = append(want, "-g", "*.txt")
	if !slices.Equal(got, want) {
		t.Fatalf("OnlyTypesGlobs() = %q, want %q", got, want)
	}
}

func TestShouldSkipDirectory(t *testing.T) {
	// Directories that must be pruned during the walk.
	for _, d := range []string{".git", ".svn", "node_modules", "vendor", "__pycache__",
		"target", "dist", ".pytest_cache", ".idea", ".vscode"} {
		if !ShouldSkipDirectory(d) {
			t.Errorf("ShouldSkipDirectory(%q) = false, want true", d)
		}
	}
	// Dot-directories that must NOT be blanket-skipped (real config/source can live here).
	for _, d := range []string{".config", ".local", ".github", "src", "docs", "app", "internal"} {
		if ShouldSkipDirectory(d) {
			t.Errorf("ShouldSkipDirectory(%q) = true, want false", d)
		}
	}
	// "build" is also a common source-document directory (for example,
	// docs/build). Skipping it by basename silently drops valid results.
	if ShouldSkipDirectory("build") {
		t.Error(`ShouldSkipDirectory("build") = true, want false`)
	}
}

func TestYamlBothExtensionsPresent(t *testing.T) {
	// Regression guard: both .yaml and .yml must be searchable as documents.
	for _, ext := range []string{"yaml", "yml"} {
		if !slices.Contains(DocumentTypes, ext) {
			t.Errorf("DocumentTypes missing %q", ext)
		}
	}
	docs := strings.Join(BuildRipgrepFileTypes(false), " ")
	if !strings.Contains(docs, "*.yaml") || !strings.Contains(docs, "*.yml") {
		t.Errorf("BuildRipgrepFileTypes(false) must include both *.yaml and *.yml; got %q", docs)
	}
}
