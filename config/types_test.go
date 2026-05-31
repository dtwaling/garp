package config

import (
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

func TestBuildRipgrepFileTypesCarriesNewCodeGlobs(t *testing.T) {
	got := strings.Join(BuildRipgrepFileTypes(true), " ")
	for _, glob := range []string{"*.pas", "*.dpr", "*.dpk", "*.inc", "*.dfm", "*.fmx", "*.dproj", "*.groupproj", "*.phtml"} {
		if !strings.Contains(got, glob) {
			t.Errorf("BuildRipgrepFileTypes(true) missing glob %q", glob)
		}
	}
	// Without --code, code globs must NOT appear.
	docsOnly := strings.Join(BuildRipgrepFileTypes(false), " ")
	if strings.Contains(docsOnly, "*.pas") {
		t.Errorf("BuildRipgrepFileTypes(false) should not include *.pas")
	}
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
