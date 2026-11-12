//go:build unix

package app

import "testing"

func TestMaxRSSBytes(t *testing.T) {
	tests := []struct {
		goos   string
		maxrss int64
		want   uint64
	}{
		{"darwin", 1536, 1536},
		{"ios", 1536, 1536},
		{"linux", 1536, 1572864},
		{"freebsd", 1536, 1572864},
		{"linux", 3 << 20, 3 << 30},
		{"linux", 0, 0},
		{"linux", -1, 0},
		{"darwin", -1, 0},
	}
	for _, tt := range tests {
		if got := maxRSSBytes(tt.maxrss, tt.goos); got != tt.want {
			t.Errorf("maxRSSBytes(%d, %q) = %d, want %d", tt.maxrss, tt.goos, got, tt.want)
		}
	}
}
