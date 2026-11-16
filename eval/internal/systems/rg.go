package systems

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Terms removes whitelisted query flags and their values. Callers must first
// validate args with queryset.Validate; this is not a second syntax validator.
func Terms(args []string) []string {
	var terms []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--distance", "-m":
			i++
		default:
			if !strings.HasPrefix(args[i], "-") {
				terms = append(terms, args[i])
			}
		}
	}
	return terms
}

// RunRG intersects whole-word, case-insensitive term matches. It retains rg's
// default binary handling and reports every surviving file, in path order.
// rootAbs and the verified-cache/validated-args contract match RunGarp.
func RunRG(rootAbs string, args []string) (Run, error) {
	start := time.Now()
	r := Run{}
	files := []string{rootAbs}
	terms := Terms(args)
	if len(terms) == 0 {
		files = nil
	}
	for _, term := range terms {
		var next []string
		for begin := 0; begin < len(files); begin += 500 {
			argv := []string{"--no-config", "-l", "-i", "-w", "--no-ignore", "--hidden", "--sort", "path"}
			if prefix, ok := strings.CutSuffix(term, "*"); ok {
				argv = append(argv, "-e", `\b`+regexp.QuoteMeta(prefix)+`[A-Za-z0-9_.-]*`)
			} else {
				argv = append(argv, "-F", term)
			}
			argv = append(argv, "--")
			argv = append(argv, files[begin:min(begin+500, len(files))]...)
			stdout, err := exec.Command("rg", argv...).Output()
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					r.Wall = time.Since(start)
					return r, fmt.Errorf("rg: %w", err)
				}
			}
			if len(stdout) != 0 {
				next = append(next, strings.Split(strings.TrimSuffix(string(stdout), "\n"), "\n")...)
			}
		}
		files = unique(next)
		sort.Strings(files)
	}
	for _, file := range files {
		r.Ranking = append(r.Ranking, strings.TrimPrefix(file, rootAbs+"/"))
	}
	r.Stdout = []byte(strings.Join(r.Ranking, "\n"))
	r.Wall = time.Since(start)
	return r, nil
}

// RGVersion records the installed baseline's version, not a harness pin.
func RGVersion() (string, error) {
	out, err := exec.Command("rg", "--version").Output()
	if err != nil {
		return "", fmt.Errorf("rg version: %w", err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return line, nil
}
