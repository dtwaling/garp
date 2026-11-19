package systems

import (
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestRunRG(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not on PATH")
	}
	root := writeCorpus(t, map[string]string{
		"a.txt": "ALPHA beta", "b.txt": "alpha BETAMAX", "c.txt": "alphabet beta",
		".hidden.txt": "alpha beta", "literal.txt": "a.b-suffix", "other.txt": "axb-suffix",
	})
	for _, tc := range []struct{ args, want []string }{
		{[]string{"alpha", "--distance", "300", "beta", "-m", "2", "--strict", "--partial=contains", "--smart-forms"}, []string{".hidden.txt", "a.txt"}},
		{[]string{"alpha", "beta*"}, []string{".hidden.txt", "a.txt", "b.txt"}},
		{[]string{"absent|alpha", "beta"}, []string{".hidden.txt", "a.txt"}},
		{[]string{"absent|beta*", "alpha"}, []string{".hidden.txt", "a.txt", "b.txt"}},
		{[]string{"a.b-suffix|absent*"}, []string{"literal.txt"}},
		{[]string{"a.b*"}, []string{"literal.txt"}},
		{[]string{"absent", "alpha"}, nil},
		{nil, nil},
	} {
		r, err := RunRG(root, tc.args)
		if err != nil || !reflect.DeepEqual(r.Ranking, tc.want) || string(r.Stdout) != strings.Join(tc.want, "\n") {
			t.Fatalf("args %v: %+v, %v; want %v", tc.args, r, err, tc.want)
		}
	}
	version, err := RGVersion()
	if err != nil || !strings.HasPrefix(version, "ripgrep ") || strings.Contains(version, "\n") {
		t.Fatalf("version %q, %v", version, err)
	}
	if _, err := RunRG(root+"/missing", []string{"alpha"}); err == nil {
		t.Fatal("rg error must not become an empty match")
	}
	// A later term must intersect every batch, then restore global path order.
	files := make(map[string]string)
	var want []string
	for i := 0; i < 503; i++ {
		name := fmt.Sprintf("%03d.txt", i)
		files[name] = "alpha beta"
		want = append(want, name)
	}
	r, err := RunRG(writeCorpus(t, files), []string{"alpha", "beta"})
	if err != nil || !reflect.DeepEqual(r.Ranking, want) {
		t.Fatalf("batched ranking: %d files, %v", len(r.Ranking), err)
	}
}
