package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"garp/search"
)

func TestRunRejectsInvalidStartDirBeforeEveryMode(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write file root fixture: %v", err)
	}

	roots := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{
			name: "missing",
			setup: func(t *testing.T) string {
				return filepath.Join(base, "missing")
			},
		},
		{
			name: "file",
			setup: func(t *testing.T) string {
				return file
			},
		},
		{
			name: "broken-link",
			setup: func(t *testing.T) string {
				link := filepath.Join(t.TempDir(), "broken-link")
				if err := os.Symlink(filepath.Join(base, "missing-target"), link); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				return link
			},
		},
		{
			name: "file-target-link",
			setup: func(t *testing.T) string {
				link := filepath.Join(t.TempDir(), "file-link")
				if err := os.Symlink(file, link); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				return link
			},
		},
	}

	modes := []struct {
		name string
		flag string
	}{
		{name: "JSON", flag: "--json"},
		{name: "plain", flag: "--plain"},
		{name: "path-only", flag: "--pathonly"},
		{name: "TUI"},
	}
	for _, rootCase := range roots {
		t.Run(rootCase.name, func(t *testing.T) {
			target := rootCase.setup(t)
			for _, mode := range modes {
				t.Run(mode.name, func(t *testing.T) {
					argv := []string{"garp", "needle", "--startdir", target}
					if mode.flag != "" {
						argv = append(argv, mode.flag)
					}
					code, stderr := runWithArgsAndStderr(t, argv)
					if code != 1 {
						t.Errorf("Run() exit code = %d, want 1", code)
					}
					if !strings.Contains(stderr, "invalid search root") {
						t.Errorf("stderr = %q, want invalid search root error", stderr)
					}
				})
			}
		})
	}
}

func TestParseArgumentsStartDirs(t *testing.T) {
	args := parseArguments([]string{
		"needle",
		"--startdir", "first, second",
	})
	if args.ParseErr != nil {
		t.Fatal(args.ParseErr)
	}
	if !args.StartDirProvided {
		t.Fatal("StartDirProvided = false")
	}
	if len(args.StartDirs) != 1 || args.StartDirs[0] != "first, second" {
		t.Fatalf("StartDirs = %q", args.StartDirs)
	}
	if args.StartDir != filepath.Clean("first") {
		t.Fatalf("StartDir = %q, want first compatibility value", args.StartDir)
	}
}

func TestRunRejectsAbsolutePolicyOutsideAllStartDirs(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "*.txt")
	code, stderr := runWithArgsAndStderr(t, []string{
		"garp", "needle", "--json",
		"--startdir", root,
		"--pathscope", outside,
	})
	if code != 1 {
		t.Fatalf("Run() exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "outside every search root") {
		t.Fatalf("stderr = %q, want containment error", stderr)
	}
}

func TestStartDirJSONPreservesRelativeReportedRoot(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "workspace")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(parent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(parent, "c", "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".ignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDirectory)

	for _, test := range []struct {
		name      string
		raw       string
		want      string
		pathScope string
	}{
		{name: "single relative root", raw: filepath.Join("workspace", "."), want: "workspace"},
		{name: "child then parent", raw: "workspace/child,workspace", want: "workspace"},
		{name: "parent then child", raw: "workspace,workspace/child", want: "workspace"},
		{name: "exemption parent then child", raw: "workspace,workspace/c", want: "workspace", pathScope: "ignored"},
		{name: "exemption child then parent", raw: "workspace/c,workspace", want: "workspace", pathScope: "ignored"},
		{
			name:      "three-level child order",
			raw:       "workspace/c/ignored,workspace/c,workspace",
			want:      "workspace",
			pathScope: "ignored,!zzz",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalArgs := os.Args
			os.Args = []string{"garp", "missing-needle", "--json", "--startdir", test.raw}
			if test.pathScope != "" {
				os.Args = append(os.Args, "--pathscope", test.pathScope)
			}
			defer func() { os.Args = originalArgs }()

			output := captureCLIStdout(t, Run)
			var decoded jsonOutput
			if err := json.Unmarshal(output, &decoded); err != nil {
				t.Fatalf("decode JSON: %v\n%s", err, output)
			}
			if decoded.Query.StartDir != filepath.Clean(test.want) {
				t.Fatalf("query.start_dir = %q, want relative %q", decoded.Query.StartDir, filepath.Clean(test.want))
			}
		})
	}
}

func TestRunRejectsMaxFileSizeErrorBeforeEveryMode(t *testing.T) {
	modes := []string{"--json", "--plain", "--pathonly", ""}
	for _, mode := range modes {
		name := mode
		if name == "" {
			name = "TUI"
		}
		t.Run(name, func(t *testing.T) {
			argv := []string{"garp", "needle"}
			if mode != "" {
				argv = append(argv, mode)
			}
			argv = append(argv, "--max-filesize", "0")
			code, stderr := runWithArgsAndStderr(t, argv)
			if code != 1 {
				t.Errorf("Run() exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr, "--max-filesize") {
				t.Errorf("stderr = %q, want max-filesize error", stderr)
			}
		})
	}
}

func TestStartDirPrepareStartRootsRejectsInvalidRoots(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write file root fixture: %v", err)
	}

	for name, path := range map[string]string{
		"missing": filepath.Join(root, "missing"),
		"file":    file,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := search.PrepareStartRoots([]string{path}, false); err == nil {
				t.Fatalf("PrepareStartRoots(%q) error = nil, want rejection", path)
			}
		})
	}

	t.Run("broken link", func(t *testing.T) {
		link := filepath.Join(root, "broken-link")
		if err := os.Symlink(filepath.Join(root, "missing-target"), link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if _, err := search.PrepareStartRoots([]string{link}, false); err == nil {
			t.Fatal("PrepareStartRoots(broken link) error = nil, want rejection")
		}
	})

	t.Run("file-target link", func(t *testing.T) {
		link := filepath.Join(root, "file-target-link")
		if err := os.Symlink(file, link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if _, err := search.PrepareStartRoots([]string{link}, false); err == nil {
			t.Fatal("PrepareStartRoots(file-target link) error = nil, want rejection")
		}
	})

	t.Run("unreadable directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod does not reliably remove directory read access on Windows")
		}
		unreadable := filepath.Join(root, "unreadable")
		if err := os.Mkdir(unreadable, 0o700); err != nil {
			t.Fatalf("create unreadable directory: %v", err)
		}
		if err := os.Chmod(unreadable, 0); err != nil {
			t.Fatalf("remove directory permissions: %v", err)
		}
		defer os.Chmod(unreadable, 0o700)
		if _, err := os.ReadDir(unreadable); err == nil {
			t.Skip("current user can still read permission-free directory")
		}
		if _, err := search.PrepareStartRoots([]string{unreadable}, false); err == nil {
			t.Fatal("PrepareStartRoots(unreadable directory) error = nil, want rejection")
		}
	})
}

func runWithArgsAndStderr(t *testing.T, argv []string) (int, string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	originalArgs := os.Args
	originalStderr := os.Stderr
	os.Args = argv
	os.Stderr = writer
	defer func() {
		os.Args = originalArgs
		os.Stderr = originalStderr
	}()

	code := Run()
	_ = writer.Close()
	out, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return code, string(out)
}
