package search

import (
	"testing"

	"garp/config"
)

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
