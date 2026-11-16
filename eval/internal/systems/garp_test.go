package systems

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	// Keep the subprocess build's dependency graph in this nested module's
	// tidy output, so go build garp works offline without modifying go.mod.
	_ "garp/app"
)

func writeCorpus(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "corpus with space")
	for name, text := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRunGarp(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "garp")
	if out, err := exec.Command("go", "build", "-o", bin, "garp").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	root := writeCorpus(t, map[string]string{"a.txt": "alpha beta", "b.txt": "alpha", "c.go": "package alpha"})
	for _, tc := range []struct {
		corpus string
		want   []string
	}{
		{"docs", []string{"a.txt", "b.txt"}},
		{"code", []string{"a.txt", "b.txt", "c.go"}},
	} {
		t.Run(tc.corpus, func(t *testing.T) {
			r, err := RunGarp(bin, tc.corpus, root, []string{"alpha"})
			if err != nil || r.Err != "" || r.TimedOut != 0 {
				t.Fatalf("run: %+v, %v", r, err)
			}
			if !reflect.DeepEqual(r.Ranking, tc.want) {
				t.Fatalf("ranking %v, want %v", r.Ranking, tc.want)
			}
			if bytes.Contains(r.Stdout, []byte(root)) {
				t.Fatalf("absolute root remains: %s", r.Stdout)
			}
			var body struct {
				Query struct {
					StartDir string `json:"start_dir"`
				} `json:"query"`
			}
			if err := json.Unmarshal(r.Stdout, &body); err != nil || body.Query.StartDir != "." {
				t.Fatalf("start dir: %+v, %v", body, err)
			}
			if r.Wall <= 0 {
				t.Fatal("missing wall time")
			}
		})
	}
	// Confirm the shipped JSON contract, not just the normalization helper.
	raw, err := exec.Command(bin, "alpha", "--json", "--startdir", root, "--no-ignore").Output()
	if err != nil || !bytes.Contains(raw, []byte(root+"/a.txt")) {
		t.Fatalf("expected absolute file path: %s, %v", raw, err)
	}
	r, err := RunGarp(bin, "docs", root, []string{"--unsupported-eval-option"})
	if err != nil || r.Err == "" || len(r.Ranking) != 0 {
		t.Fatalf("nonzero exit: %+v, %v", r, err)
	}
	if _, err := RunGarp(filepath.Join(root, "missing"), "docs", root, []string{"alpha"}); err == nil {
		t.Fatal("missing executable is a setup error")
	}
}

func TestNormalizeGarp(t *testing.T) {
	for _, root := range []string{"/cache/docs", "/cache/quote\"and\\slash<>&"} {
		var input bytes.Buffer
		enc := json.NewEncoder(&input)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(map[string]any{"query": map[string]string{"start_dir": root}, "results": []map[string]any{{"file": root + "/a.txt", "excerpts": []map[string]any{{"text": root + "/b.txt", "start_line": 2}}}}}); err != nil {
			t.Fatal(err)
		}
		got := normalizeGarp(input.Bytes(), root)
		var body map[string]any
		if err := json.Unmarshal(got, &body); err != nil {
			t.Fatal(err)
		}
		if body["query"].(map[string]any)["start_dir"] != "." || bytes.Contains(got, []byte(root)) {
			t.Fatalf("normalization: %s", got)
		}
		if !bytes.Contains(got, []byte(`"file":"a.txt"`)) || !bytes.Contains(got, []byte(`"text":"b.txt"`)) {
			t.Fatalf("prefix not stripped: %s", got)
		}
	}
}

func TestGarpResponseProcess(t *testing.T) {
	if response, ok := os.LookupEnv("GARP_TEST_RESPONSE"); ok {
		fmt.Print(response)
		os.Exit(0)
	}
}

func TestRunGarpResponses(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		output   string
		want     []string
		timedOut int
		bad      bool
	}{
		{`{"results":[{"file":"/root/b"},{"file":"/root/a"},{"file":"/root/b"}]}`, []string{"b", "a"}, 0, false},
		{`{"results":[],"timed_out":1}`, nil, 1, true},
		{`not JSON`, nil, 0, true},
		{`{"results":null}`, nil, 0, false},
	} {
		t.Setenv("GARP_TEST_RESPONSE", tc.output)
		r, err := RunGarp(bin, "docs", "/root", []string{"-test.run=^TestGarpResponseProcess$", "--"})
		if (err != nil) != tc.bad || r.TimedOut != tc.timedOut || !reflect.DeepEqual(r.Ranking, tc.want) {
			t.Fatalf("response %q: %+v, %v", tc.output, r, err)
		}
	}
}
