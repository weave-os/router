//go:build !windows

package trafficcapture

import (
	"fmt"
	"os"
)

// createSpoolFile opens an anonymous temporary file: the name is unlinked
// while the handle stays open, so the captured bytes are reclaimed on close
// and no payload is left behind if the process exits early.
func createSpoolFile() (*os.File, error) {
	bodyFile, err := os.CreateTemp("", spoolFilePattern)
	if err != nil {
		return nil, fmt.Errorf("create temporary HTTP body capture: %w", err)
	}
	name := bodyFile.Name()
	if err := os.Remove(name); err != nil {
		_ = bodyFile.Close()
		_ = os.Remove(name)
		return nil, fmt.Errorf("unlink temporary HTTP body capture: %w", err)
	}
	return bodyFile, nil
}

// removeSpoolFile is a no-op because createSpoolFile already unlinked the name.
func removeSpoolFile(*os.File) error { return nil }
