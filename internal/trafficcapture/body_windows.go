//go:build windows

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
	return bodyFile, nil
}

func removeSpoolFile(bodyFile *os.File) error {
	return os.Remove(bodyFile.Name())
}
