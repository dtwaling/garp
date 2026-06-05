//go:build unix

package app

import (
	"time"

	"golang.org/x/sys/unix"
)

func currentProcessUsage() (rss uint64, proc time.Duration) {
	var rusage unix.Rusage
	_ = unix.Getrusage(unix.RUSAGE_SELF, &rusage)

	rss = uint64(rusage.Maxrss * 1024) // KB to bytes
	user := time.Duration(rusage.Utime.Sec)*time.Second + time.Duration(rusage.Utime.Usec)*time.Microsecond
	sys := time.Duration(rusage.Stime.Sec)*time.Second + time.Duration(rusage.Stime.Usec)*time.Microsecond
	return rss, user + sys
}
