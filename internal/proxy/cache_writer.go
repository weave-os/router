package proxy

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/sse"
)

type captureWriter struct {
	w           http.ResponseWriter
	body        bytes.Buffer
	statusCode  int
	wroteHeader bool
	maxBytes    int
	overflow    bool
}

func newCaptureWriter(w http.ResponseWriter, maxBytes int) *captureWriter {
	return &captureWriter{w: w, statusCode: http.StatusOK, maxBytes: maxBytes}
}

func (c *captureWriter) Header() http.Header { return c.w.Header() }

func (c *captureWriter) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}
	c.statusCode = code
	c.wroteHeader = true
	sse.PrepareJSONResponse(c.w)
	c.w.WriteHeader(code)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if !c.overflow {
		if c.body.Len()+len(p) > c.maxBytes {
			c.overflow = true
			c.body.Reset()
		} else {
			c.body.Write(p)
		}
	}
	return sse.WriteJSONChunk(c.w, p)
}

func (c *captureWriter) Flush() {
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *captureWriter) captured() ([]byte, int, bool) {
	if c.overflow {
		return nil, 0, false
	}
	out := make([]byte, c.body.Len())
	copy(out, c.body.Bytes())
	return out, c.statusCode, true
}
