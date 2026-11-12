//go:build unix

package app

import (
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

func currentProcessUsage() (rss uint64, proc time.Duration) {
	var rusage unix.Rusage
	_ = unix.Getrusage(unix.RUSAGE_SELF, &rusage)

	rss = maxRSSBytes(int64(rusage.Maxrss), runtime.GOOS)
	user := time.Duration(rusage.Utime.Sec)*time.Second + time.Duration(rusage.Utime.Usec)*time.Microsecond
	sys := time.Duration(rusage.Stime.Sec)*time.Second + time.Duration(rusage.Stime.Usec)*time.Microsecond
	return rss, user + sys
}

// maxRSSBytes converts getrusage(2) ru_maxrss to bytes; Darwin and iOS report
// bytes, Linux and the BSDs report KiB.
func maxRSSBytes(maxrss int64, goos string) uint64 {
	if maxrss <= 0 {
		return 0
	}
	if goos == "darwin" || goos == "ios" {
		return uint64(maxrss)
	}
	return uint64(maxrss) * 1024
}
