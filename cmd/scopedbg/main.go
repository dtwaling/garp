// scopedbg: diagnostic tool for --pathscope matching.
// Usage: scopedbg <walkRoot> <pathscope_csv> [file1 file2 ...]
// If no files given, walks the tree and prints match/skip for each file.
//
// Build: go build -o bin/scopedbg ./cmd/scopedbg
// Run:   ./bin/scopedbg /mnt/bro/thinktank/audio2midi 'audio2midi/*,tests/*' 
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func matchesPathScope(absPath, walkRoot string, pathScope []string) (bool, string) {
	if len(pathScope) == 0 {
		return true, "no-scope"
	}
	rel, err := filepath.Rel(walkRoot, absPath)
	if err != nil {
		return false, fmt.Sprintf("Rel() error: %v", err)
	}
	relSlash := filepath.ToSlash(rel)
	for _, pattern := range pathScope {
		if matched, err := filepath.Match(pattern, relSlash); err == nil && matched {
			return true, fmt.Sprintf("glob match pattern=%q rel=%q", pattern, relSlash)
		}
		if !strings.ContainsAny(pattern, "*?") {
			prefix := strings.TrimRight(pattern, "/")
			if prefix != "" && (relSlash == prefix || strings.HasPrefix(relSlash, prefix+"/")) {
				return true, fmt.Sprintf("prefix match pattern=%q rel=%q", pattern, relSlash)
			}
		}
	}
	return false, fmt.Sprintf("no match rel=%q patterns=%v", relSlash, pathScope)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: scopedbg <walkRoot> <pathscope_csv> [files...]\n")
		os.Exit(1)
	}
	walkRoot, err := filepath.Abs(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad walkRoot: %v\n", err)
		os.Exit(1)
	}
	patterns := strings.Split(os.Args[2], ",")
	for i, p := range patterns {
		patterns[i] = strings.TrimSpace(p)
	}

	fmt.Printf("walkRoot : %s\n", walkRoot)
	fmt.Printf("patterns : %v\n", patterns)
	fmt.Println()

	if len(os.Args) > 3 {
		// Explicit file list
		for _, f := range os.Args[3:] {
			abs, _ := filepath.Abs(f)
			match, reason := matchesPathScope(abs, walkRoot, patterns)
			fmt.Printf("[%v] %s  (%s)\n", match, abs, reason)
		}
		return
	}

	// Walk tree
	matched := 0
	skipped := 0
	filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		match, reason := matchesPathScope(path, walkRoot, patterns)
		sym := "SKIP"
		if match {
			sym = "MATCH"
			matched++
		} else {
			skipped++
		}
		fmt.Printf("[%s] %s  (%s)\n", sym, path, reason)
		return nil
	})
	fmt.Printf("\n%d matched, %d skipped\n", matched, skipped)
}
