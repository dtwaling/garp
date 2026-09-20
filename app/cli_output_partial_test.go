package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONOutputPartial(t *testing.T) {
	root := t.TempDir()
	writeCLIOutputFixture(t, root, "deployment.md", "deployment completed successfully\n")

	for _, tc := range []struct {
		name     string
		partial  string
		wantJSON string
		wantMode string
	}{
		{name: "prefix", partial: "--partial=prefix", wantJSON: "prefix", wantMode: "Mode: partial (prefix)"},
		{name: "contains", partial: "--partial=contains", wantJSON: "contains", wantMode: "Mode: partial (contains)"},
		{name: "off omits partial", partial: "", wantJSON: "", wantMode: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{"deploy", "--startdir", root, "--only", "md"}
			if tc.partial != "" {
				argv = append(argv, tc.partial)
			}
			args := parseArguments(argv)

			jsonOut := captureCLIStdout(t, func() int { return runJSON(args) })
			var output struct {
				Query map[string]json.RawMessage `json:"query"`
			}
			if err := json.Unmarshal(jsonOut, &output); err != nil {
				t.Fatalf("decode JSON output: %v", err)
			}
			partial, present := output.Query["partial"]
			if tc.wantJSON == "" {
				if present {
					t.Errorf("JSON query partial = %s, want omitted", partial)
				}
			} else {
				var got string
				if !present || json.Unmarshal(partial, &got) != nil || got != tc.wantJSON {
					t.Errorf("JSON query partial = %s, want %q", partial, tc.wantJSON)
				}
			}

			plainOut := string(captureCLIStdout(t, func() int { return runPlain(args) }))
			if tc.wantMode == "" {
				if strings.Contains(plainOut, "Mode: partial (") {
					t.Errorf("plain output unexpectedly includes partial mode:\n%s", plainOut)
				}
			} else if !strings.Contains(plainOut, tc.wantMode) {
				t.Errorf("plain output missing partial mode %q:\n%s", tc.wantMode, plainOut)
			}
		})
	}
}

func writeCLIOutputFixture(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %q: %v", name, err)
	}
}
