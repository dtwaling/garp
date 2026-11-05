//go:build !windows

package search

import "testing"

func TestUnixFilesystemRootAbsoluteCoverage(t *testing.T) {
	root := StartRoot{
		LogicalPath:  "/",
		SuppliedPath: "/",
		PhysicalPath: "/",
		CanonicalKey: "/",
		Supplied:     true,
		Reported:     true,
	}
	pathScope := []string{"/home/**"}
	if err := ValidateRootPolicies([]StartRoot{root}, pathScope, nil); err != nil {
		t.Fatalf("filesystem-root validation rejected contained pattern: %v", err)
	}
	policy, err := CompilePathPolicy(pathScope, nil, "/", "/")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.CouldMatchRoot() {
		t.Fatal("filesystem root was pruned before contained absolute pattern")
	}
}
