//go:build !windows

package main

import (
	"os"
	"syscall"
)

// openCaptureFile opens the JSONL capture file with O_NOFOLLOW so a symlinked
// capture path fails instead of redirecting captured exchanges into the link
// target. O_NONBLOCK keeps startup from hanging when the path is a fifo that
// has no reader yet.
func openCaptureFile(path string) (*os.File, error) {
	return os.OpenFile(
		path,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,
		trafficCaptureFilePermissions,
	)
}
