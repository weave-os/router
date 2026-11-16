//go:build !windows

package trafficcapture

import (
	"fmt"
	"os"
)

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

func removeSpoolFile(*os.File) error { return nil }
