//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// openCaptureFile opens the JSONL capture file. Windows exposes no O_NOFOLLOW
// equivalent in syscall, and CreateFile follows reparse points, so a symlinked
// capture path is rejected before the open instead of being redirected into the
// link target. The check is a pre-open inspection, so it closes the accidental
// redirection case rather than a determined local race.
//
// Any path that cannot be inspected is refused rather than opened, so a failed
// check can never fall through to a following-open.
func openCaptureFile(path string) (*os.File, error) {
	switch info, err := os.Lstat(path); {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return nil, fmt.Errorf("capture path %q is a symbolic link", path)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("inspect capture path %q: %w", path, err)
	}
	return os.OpenFile(
		path,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		trafficCaptureFilePermissions,
	)
}
