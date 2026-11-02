package app

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestStartDirValidateSearchRootRejectsInvalidRoots(t *testing.T) {
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
			if err := validateSearchRoot(path); err == nil {
				t.Fatalf("validateSearchRoot(%q) error = nil, want rejection", path)
			}
		})
	}

	t.Run("broken link", func(t *testing.T) {
		link := filepath.Join(root, "broken-link")
		if err := os.Symlink(filepath.Join(root, "missing-target"), link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if err := validateSearchRoot(link); err == nil {
			t.Fatal("validateSearchRoot(broken link) error = nil, want rejection")
		}
	})

	t.Run("file-target link", func(t *testing.T) {
		link := filepath.Join(root, "file-target-link")
		if err := os.Symlink(file, link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if err := validateSearchRoot(link); err == nil {
			t.Fatal("validateSearchRoot(file-target link) error = nil, want rejection")
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
		if err := validateSearchRoot(unreadable); err == nil {
			t.Fatal("validateSearchRoot(unreadable directory) error = nil, want rejection")
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
