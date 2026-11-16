package trafficcapture

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
)

const spoolFilePattern = "router-http-capture-body-*"

type BodySpool struct {
	mu     sync.Mutex
	file   *os.File
	err    error
	closed bool
}

func (spool *BodySpool) Write(body []byte) {
	if len(body) == 0 {
		return
	}

	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.closed || spool.err != nil {
		return
	}
	if spool.file == nil {
		bodyFile, err := createSpoolFile()
		if err != nil {
			spool.err = err
			return
		}
		spool.file = bodyFile
	}
	written, err := spool.file.Write(body)
	if err == nil && written != len(body) {
		err = io.ErrShortWrite
	}
	if err != nil {
		spool.err = fmt.Errorf("write temporary HTTP body capture: %w", err)
	}
}

func (spool *BodySpool) Reader() (io.Reader, error) {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.file == nil {
		return bytes.NewReader(nil), nil
	}
	fileInfo, err := spool.file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat temporary HTTP body capture: %w", err)
	}
	return io.NewSectionReader(spool.file, 0, fileInfo.Size()), nil
}

func (spool *BodySpool) Err() error {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	return spool.err
}

func (spool *BodySpool) Close() error {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	if spool.closed {
		return nil
	}
	spool.closed = true
	if spool.file == nil {
		return nil
	}
	if err := spool.file.Close(); err != nil {
		return fmt.Errorf("close temporary HTTP body capture: %w", err)
	}
	if err := removeSpoolFile(spool.file); err != nil {
		return fmt.Errorf("remove temporary HTTP body capture: %w", err)
	}
	return nil
}
