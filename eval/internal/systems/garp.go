// Package systems runs the retrieval baselines on verified corpus subtrees.
package systems

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Run keeps normalized output bytes intact for token counts and hit@N.
// Err records a garp process exit (a scored miss); returned errors instead
// indicate setup, decoding or timeout failures that must stop the harness.
type Run struct {
	Ranking  []string
	Stdout   []byte
	TimedOut int
	Err      string
	Wall     time.Duration
}

// RunGarp runs either garp version once, with stdin closed. rootAbs is the
// absolute logical corpus subtree, without resolving symlinks. args must have
// passed queryset.Validate; the cache must contain only verified manifest files.
func RunGarp(bin, corpus, rootAbs string, args []string) (Run, error) {
	argv := append([]string(nil), args...)
	argv = append(argv, "--json", "--startdir", rootAbs, "--no-ignore", "--file-timeout-binary", "60000")
	if corpus == "code" {
		argv = append(argv, "--code")
	}
	cmd := exec.Command(bin, argv...)
	start := time.Now()
	stdout, err := cmd.Output()
	r := Run{Stdout: normalizeGarp(stdout, rootAbs), Wall: time.Since(start)}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			r.Err = exit.Error()
			return r, nil
		}
		return r, fmt.Errorf("start garp: %w", err)
	}
	var body struct {
		Results []struct {
			File     string `json:"file"`
			Excerpts []struct {
				StartLine int `json:"start_line"`
			} `json:"excerpts"`
		} `json:"results"`
		TimedOut int `json:"timed_out"`
	}
	if err := json.Unmarshal(r.Stdout, &body); err != nil {
		return r, fmt.Errorf("decode garp: %w", err)
	}
	r.TimedOut = body.TimedOut
	if r.TimedOut > 0 {
		return r, fmt.Errorf("garp timed_out: %d", r.TimedOut)
	}
	for _, result := range body.Results {
		r.Ranking = append(r.Ranking, result.File)
	}
	r.Ranking = unique(r.Ranking)
	return r, nil
}

// normalizeGarp preserves original JSON whitespace and order. Strip the encoded
// root/ everywhere first, then replace complete root string tokens with "."
// (not substrings inside quoted excerpt text), including query.start_dir.
func normalizeGarp(stdout []byte, rootAbs string) []byte {
	// Match app/cli.go's JSON encoder, which uses SetEscapeHTML(false).
	encode := func(s string) []byte {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(s)
		return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	}
	prefix := encode(rootAbs + "/")
	stdout = bytes.ReplaceAll(stdout, prefix[1:len(prefix)-1], nil)
	root := encode(rootAbs)
	var out []byte
	last := 0
	for i := 0; i < len(stdout); i++ {
		if stdout[i] != '"' {
			continue
		}
		start := i
		for i++; i < len(stdout); i++ {
			if stdout[i] == '\\' {
				i++
				continue
			}
			if stdout[i] == '"' {
				break
			}
		}
		if i < len(stdout) && bytes.Equal(stdout[start:i+1], root) {
			out = append(out, stdout[last:start]...)
			out = append(out, '"', '.', '"')
			last = i + 1
		}
	}
	return append(out, stdout[last:]...)
}

func unique(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	var out []string
	for _, p := range paths {
		if !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out
}
