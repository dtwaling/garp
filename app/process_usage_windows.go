//go:build windows

package app

import "time"

func currentProcessUsage() (rss uint64, proc time.Duration) {
	return 0, 0
}
