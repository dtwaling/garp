# garp -- `--startdir` + `--pathscope` Implementation Plan

> **Branch:** feature/startdir-pathscope

**Goal:** Add two optional CLI args to garp that let Optimus-v2 pin the search
root and narrow scope to specific subpaths, safely, without creating shell/cmd
injection surfaces.

**Architecture:**
- `search/scope.go` -- pure validation and path-normalization logic (no I/O).
  All security-sensitive logic lives here: tested in isolation before any
  wire-up.
- `config/types.go` -- no changes (extension types live here; untouched).
- `app/cli.go` -- parse the two new flags into `Arguments`, thread into model.
- `app/tui.go` -- `model` struct gets `startDir` and `pathScope` fields; pass
  to search engine.
- `search/engine.go` -- `SearchEngine` struct gets `StartDir` and `PathScope`
  fields; `DiscoverCandidates` passes them to the walk functions.
- `search/filter.go` -- `GetDocumentFileCount`, `FindFilesWithFirstWord`, and
  `FindFilesWithFirstWordProgress` get `walkRoot string` and `pathScope
  []string` params. Walk from `walkRoot` instead of `"."`. File paths are
  filtered against `pathScope` patterns before processing.

**Task order (strict -- each task's tests must pass before the next starts):**

| # | Task | File(s) | Test file |
|---|------|---------|-----------|
| 1 | ValidateStartDir | search/scope.go | search/scope_test.go |
| 2 | ValidatePathScope | search/scope.go | search/scope_test.go |
| 3 | Walk accepts walkRoot | search/filter.go | search/filter_scope_test.go |
| 4 | Walk filters by pathScope | search/filter.go | search/filter_scope_test.go |
| 5 | Thread StartDir through SearchEngine | search/engine.go | existing tests + new |
| 6 | CLI parsing -- --startdir | app/cli.go | app/cli_scope_test.go |
| 7 | CLI parsing -- --pathscope | app/cli.go | app/cli_scope_test.go |
| 8 | Help text update | app/cli.go | manual verify |

---

## Task 1 -- ValidateStartDir

**Objective:** Pure function: takes a raw string from CLI, returns a cleaned
absolute path (trailing slash normalized) or an error.

**Security rules enforced:**
- Reject any string containing: `*`, `?`, `[`, `]`, `{`, `}`, `|`, `;`, `&`,
  `$`, `` ` ``, `>`, `<`, `\n`, `\r`
- Accept Windows (`C:\`, `C:/`), Mac, and Linux paths
- Normalize: filepath.Clean + ensure trailing slash representation is absent
  in the returned path (os.Stat can verify it's actually a directory)

**File: `search/scope.go`**

```go
package search

import (
    "fmt"
    "path/filepath"
    "strings"
)

// wildcardAndInjectionChars are characters rejected in path arguments to
// prevent shell injection and unintended glob expansion.
var wildcardAndInjectionChars = []string{
    "*", "?", "[", "]", "{", "}", "|", ";", "&",
    "$", "`", ">", "<", "\n", "\r",
}

// ValidateStartDir validates and normalizes a --startdir argument.
// Returns the cleaned absolute path or an error describing the rejection reason.
// The caller is responsible for verifying the path is an existing directory.
func ValidateStartDir(raw string) (string, error) {
    if raw == "" {
        return "", fmt.Errorf("--startdir: path must not be empty")
    }
    for _, ch := range wildcardAndInjectionChars {
        if strings.Contains(raw, ch) {
            return "", fmt.Errorf("--startdir: rejected -- path contains illegal character %q", ch)
        }
    }
    // Normalize: clean the path (removes redundant separators, .., etc.)
    cleaned := filepath.Clean(raw)
    return cleaned, nil
}
```

**File: `search/scope_test.go`** (write first -- RED, then implement -- GREEN)

```go
package search_test

import (
    "strings"
    "testing"

    "find-words/search"
)

// --- ValidateStartDir tests ---

func TestValidateStartDir_ValidLinux(t *testing.T) {
    got, err := search.ValidateStartDir("/home/user/projects/myrepo")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got == "" {
        t.Fatal("expected non-empty path")
    }
}

func TestValidateStartDir_ValidWithTrailingSlash(t *testing.T) {
    got, err := search.ValidateStartDir("/home/user/projects/")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    // Cleaned path should not have trailing slash
    if strings.HasSuffix(got, "/") {
        t.Errorf("expected no trailing slash, got %q", got)
    }
}

func TestValidateStartDir_ValidWindows(t *testing.T) {
    // Just validate -- don't stat (path may not exist on Linux CI)
    _, err := search.ValidateStartDir(`C:\Users\dustin\projects\myrepo`)
    if err != nil {
        t.Fatalf("unexpected error for Windows path: %v", err)
    }
}

func TestValidateStartDir_RejectsWildcard(t *testing.T) {
    _, err := search.ValidateStartDir("/home/user/proj*")
    if err == nil {
        t.Fatal("expected error for wildcard, got nil")
    }
}

func TestValidateStartDir_RejectsSemicolon(t *testing.T) {
    _, err := search.ValidateStartDir("/home/user;rm -rf /")
    if err == nil {
        t.Fatal("expected error for semicolon injection, got nil")
    }
}

func TestValidateStartDir_RejectsAmpersand(t *testing.T) {
    _, err := search.ValidateStartDir("/home/user& bad")
    if err == nil {
        t.Fatal("expected error for ampersand, got nil")
    }
}

func TestValidateStartDir_RejectsPipe(t *testing.T) {
    _, err := search.ValidateStartDir("/home/user|cat /etc/passwd")
    if err == nil {
        t.Fatal("expected error for pipe, got nil")
    }
}

func TestValidateStartDir_RejectsBacktick(t *testing.T) {
    _, err := search.ValidateStartDir("/home/user`whoami`")
    if err == nil {
        t.Fatal("expected error for backtick, got nil")
    }
}

func TestValidateStartDir_RejectsDollarSign(t *testing.T) {
    _, err := search.ValidateStartDir("/home/$HOME")
    if err == nil {
        t.Fatal("expected error for dollar sign, got nil")
    }
}

func TestValidateStartDir_EmptyRejected(t *testing.T) {
    _, err := search.ValidateStartDir("")
    if err == nil {
        t.Fatal("expected error for empty path, got nil")
    }
}

func TestValidateStartDir_PathWithSpaces(t *testing.T) {
    // Spaces are valid in paths (quoted on CLI)
    _, err := search.ValidateStartDir("/home/user/my projects/repo")
    if err != nil {
        t.Fatalf("unexpected error for path with spaces: %v", err)
    }
}
```

---

## Task 2 -- ValidatePathScope

**Objective:** Validates the comma-separated `--pathscope` list. Each segment
is a simple glob pattern for path matching (only `*` and `?` allowed as
wildcards). Rejects regex metacharacters and anything with `.` that looks like
a file extension filter.

**Security rules enforced:**
- Reject: `[`, `]`, `{`, `}`, `|`, `;`, `&`, `$`, `` ` ``, `>`, `<`, `\n`,
  `\r`, `(`, `)`, `^`, `+`, `\`
- Reject any segment containing `.` -- extensions belong to `--not`/`--only`
- Allow only: alphanumeric, `/`, `\`, `-`, `_`, `*`, `?`, space, `,`
- Split on comma; trim whitespace from each segment
- Return empty slice if input is empty (feature disabled)

**Add to `search/scope.go`:**

```go
// pathScopeRejectedChars are characters rejected in --pathscope segments.
// Allows *, ? (simple globs), path separators, alphanumeric, dash, underscore.
var pathScopeRejectedChars = []string{
    "[", "]", "{", "}", "|", ";", "&", "$", "`",
    ">", "<", "\n", "\r", "(", ")", "^", "+", "\\",
}

// ValidatePathScope validates and parses a --pathscope argument.
// Input is a comma-separated list of simple glob patterns.
// Returns the parsed segments or an error.
// An empty input returns a nil slice (feature disabled).
func ValidatePathScope(raw string) ([]string, error) {
    if raw == "" {
        return nil, nil
    }
    parts := strings.Split(raw, ",")
    result := make([]string, 0, len(parts))
    for _, p := range parts {
        seg := strings.TrimSpace(p)
        if seg == "" {
            continue
        }
        // Reject regex and injection chars
        for _, ch := range pathScopeRejectedChars {
            if strings.Contains(seg, ch) {
                return nil, fmt.Errorf(
                    "--pathscope: segment %q contains illegal character %q -- "+
                        "use simple wildcards (* and ?) only; regex patterns are not supported",
                    seg, ch,
                )
            }
        }
        // Reject extension-like patterns (any segment containing '.')
        if strings.Contains(seg, ".") {
            return nil, fmt.Errorf(
                "--pathscope: segment %q contains '.' -- "+
                    "do not include file extensions; use --not or --only for extension filtering",
                seg,
            )
        }
        result = append(result, seg)
    }
    if len(result) == 0 {
        return nil, nil
    }
    return result, nil
}
```

**Add to `search/scope_test.go`:**

```go
// --- ValidatePathScope tests ---

func TestValidatePathScope_Empty(t *testing.T) {
    got, err := search.ValidatePathScope("")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if got != nil {
        t.Fatalf("expected nil for empty input, got %v", got)
    }
}

func TestValidatePathScope_SingleSegment(t *testing.T) {
    got, err := search.ValidatePathScope("*/backend/*")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(got) != 1 || got[0] != "*/backend/*" {
        t.Fatalf("expected [*/backend/*], got %v", got)
    }
}

func TestValidatePathScope_MultipleSegments(t *testing.T) {
    got, err := search.ValidatePathScope("*/backend/*/Assembly,tests/*")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(got) != 2 {
        t.Fatalf("expected 2 segments, got %d: %v", len(got), got)
    }
    if got[0] != "*/backend/*/Assembly" {
        t.Errorf("expected first segment '*/backend/*/Assembly', got %q", got[0])
    }
    if got[1] != "tests/*" {
        t.Errorf("expected second segment 'tests/*', got %q", got[1])
    }
}

func TestValidatePathScope_TrimsWhitespace(t *testing.T) {
    got, err := search.ValidatePathScope("  */backend/* ,  tests/*  ")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(got) != 2 {
        t.Fatalf("expected 2 segments, got %v", got)
    }
}

func TestValidatePathScope_RejectsExtension(t *testing.T) {
    _, err := search.ValidatePathScope("*/backend/*.cs")
    if err == nil {
        t.Fatal("expected error for extension in pathscope, got nil")
    }
    if !strings.Contains(err.Error(), "do not include file extensions") {
        t.Errorf("expected extension hint in error, got: %v", err)
    }
}

func TestValidatePathScope_RejectsRegexBracket(t *testing.T) {
    _, err := search.ValidatePathScope("*/backend/[Aa]ssembly")
    if err == nil {
        t.Fatal("expected error for regex bracket, got nil")
    }
}

func TestValidatePathScope_RejectsSemicolon(t *testing.T) {
    _, err := search.ValidatePathScope("*/backend;rm -rf")
    if err == nil {
        t.Fatal("expected error for semicolon injection, got nil")
    }
}

func TestValidatePathScope_RejectsParenthesis(t *testing.T) {
    _, err := search.ValidatePathScope("*/back(end)/*")
    if err == nil {
        t.Fatal("expected error for parenthesis (regex group), got nil")
    }
}

func TestValidatePathScope_RejectsCaret(t *testing.T) {
    _, err := search.ValidatePathScope("^backend/*")
    if err == nil {
        t.Fatal("expected error for caret (regex anchor), got nil")
    }
}

func TestValidatePathScope_QuestionMarkAllowed(t *testing.T) {
    got, err := search.ValidatePathScope("*/backe?d/*")
    if err != nil {
        t.Fatalf("unexpected error for ? wildcard: %v", err)
    }
    if len(got) == 0 {
        t.Fatal("expected non-empty result")
    }
}
```

---

## Task 3 -- Walk accepts walkRoot

**Objective:** `GetDocumentFileCount`, `FindFilesWithFirstWord`, and
`FindFilesWithFirstWordProgress` each gain a `walkRoot string` parameter.
When non-empty, `filepath.WalkDir` uses it instead of `"."`. All callers
updated.

**Mechanical change, no behavioral change when walkRoot == "".**

---

## Task 4 -- Walk filters by pathScope

**Objective:** Each walk function gains a `pathScope []string` parameter.
When non-empty, a file path is accepted only if it matches at least one
pathScope pattern via `filepath.Match`.

**Match semantics:** Each pathScope segment is matched against the file's path
relative to the walkRoot (using `filepath.ToSlash` for cross-platform
consistency). `filepath.Match(pattern, relPath)` returns true on any match.

---

## Task 5 -- Thread StartDir through SearchEngine

**Objective:** `SearchEngine` struct gets `StartDir string` and `PathScope
[]string`. `DiscoverCandidates` passes them to walk functions.

---

## Task 6 -- CLI parsing: --startdir

**Objective:** `Arguments` struct gets `StartDir string`. `parseArguments`
handles `--startdir <value>` as the next token. If validation fails, print
error to stderr and return `--startdir` error in the Arguments error field
(or exit early). Thread into `model` and `SearchEngine`.

---

## Task 7 -- CLI parsing: --pathscope

**Objective:** `Arguments` struct gets `PathScope []string`. `parseArguments`
handles `--pathscope <value>` as the next token. Validation is via
`ValidatePathScope`. Thread into `model` and `SearchEngine`.

---

## Task 8 -- Help text

**Objective:** `showUsage()` includes both new flags with clear, accurate
help text. `--pathscope` explicitly says "Do not include file extensions."

---

## Commit Message Convention

```
feat(scope): <what changed>

<Why it was needed, what security property it enforces>
```
