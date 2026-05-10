package app_test

import (
	"testing"
)

// parseArgumentsForTest is a thin wrapper so we can test parseArguments.
// parseArguments is unexported; we test via the exported surface or
// by putting these tests in package app (see cli_scope_test_internal).
// We exercise it indirectly through the exported Arguments type.
// Note: these tests live in the app package (package app, not app_test)
// so they can call parseArguments directly. See cli_scope_test_internal_test.go.

// This file intentionally empty -- see cli_scope_internal_test.go
var _ = testing.T{}
