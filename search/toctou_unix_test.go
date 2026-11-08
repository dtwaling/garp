//go:build unix

package search

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestTOCTOUFIFOReplacement(t *testing.T) {
	// Isolate possible blocking opens in a killable subprocess. A regression
	// must fail promptly without leaking blocked goroutines into the test suite.
	const childEnv = "GARP_TOCTOU_FIFO_CHILD"
	if os.Getenv(childEnv) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTOCTOUFIFOReplacement$", "-test.v")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("FIFO stage opens hung without a writer: %v\n%s", ctx.Err(), output)
		}
		if err != nil {
			t.Fatalf("FIFO stage tests failed: %v\n%s", err, output)
		}
		t.Logf("FIFO subprocess assertions:\n%s", output)
		return
	}
	runTOCTOUStages(t, func(t *testing.T, candidate string, _ []byte) {
		if err := os.Remove(candidate); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(candidate, 0600); err != nil {
			t.Fatal(err)
		}
	})
}
