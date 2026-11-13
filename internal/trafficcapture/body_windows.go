//go:build windows

package trafficcapture

import (
	"fmt"
	"os"
)

// createSpoolFile opens a temporary file that keeps its name until the spool
// closes. Windows refuses to unlink a file whose handle is still open --
// os.CreateTemp opens it without FILE_SHARE_DELETE -- so the unlink that makes
// the Unix spool anonymous cannot run at creation time here.
func createSpoolFile() (*os.File, error) {
	bodyFile, err := os.CreateTemp("", spoolFilePattern)
	if err != nil {
		return nil, fmt.Errorf("create temporary HTTP body capture: %w", err)
	}
	return bodyFile, nil
}

// removeSpoolFile deletes the temporary file once its handle has been closed.
func removeSpoolFile(bodyFile *os.File) error {
	return os.Remove(bodyFile.Name())
}
