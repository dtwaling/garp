package app

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"garp/search"
)

func TestConcurrencyFlagBounds(t *testing.T) {
	for _, tc := range []struct {
		flag    string
		maximum int
		get     func(*Arguments) int
	}{
		{"--workers", search.MaxWorkers, func(a *Arguments) int { return a.FilterWorkers }},
		{"-workers", search.MaxWorkers, func(a *Arguments) int { return a.FilterWorkers }},
		{"--heavy-concurrency", search.MaxHeavyConcurrency, func(a *Arguments) int { return a.HeavyConcurrency }},
		{"--file-timeout-binary", search.MaxFileTimeoutBinary, func(a *Arguments) int { return a.FileTimeoutBinary }},
	} {
		for _, value := range []int{-1, 0, 1, tc.maximum / 2, tc.maximum, tc.maximum + 1, math.MaxInt} {
			for _, attached := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/attached=%v", tc.flag, value, attached), func(t *testing.T) {
					args := []string{tc.flag, strconv.Itoa(value), "alpha"}
					if attached {
						args = []string{tc.flag + "=" + strconv.Itoa(value), "alpha"}
					}
					got := parseArguments(args)
					if value < 1 || value > tc.maximum {
						if got.ParseErr == nil || !strings.Contains(got.ParseErr.Error(), tc.flag) {
							t.Fatalf("expected flag-specific error, got %v", got.ParseErr)
						}
						if value > tc.maximum && !strings.Contains(got.ParseErr.Error(), fmt.Sprintf("at most %d", tc.maximum)) {
							t.Fatalf("missing upper bound: %v", got.ParseErr)
						}
						if tc.get(got) > tc.maximum {
							t.Fatalf("invalid value retained: %d", tc.get(got))
						}
					} else if got.ParseErr != nil || tc.get(got) != value {
						t.Fatalf("valid value: got %d, error %v; want %d", tc.get(got), got.ParseErr, value)
					}
				})
			}
		}
	}
}

func TestConcurrencyFlagDefaults(t *testing.T) {
	got := parseArguments([]string{"alpha"})
	if got.ParseErr != nil || got.FilterWorkers != 4 || got.FileTimeoutBinary != 1000 || got.HeavyConcurrency < 1 || got.HeavyConcurrency > search.MaxHeavyConcurrency {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}
