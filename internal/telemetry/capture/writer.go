package capture

import (
	"bytes"
	"net/http"

	"weave-os/router/internal/sse"
)

type Writer struct {
	w           http.ResponseWriter
	body        bytes.Buffer
	statusCode  int
	wroteHeader bool
	maxBytes    int
	overflow    bool
}

func NewWriter(w http.ResponseWriter, maxBytes int) *Writer {
	return &Writer{w: w, statusCode: http.StatusOK, maxBytes: maxBytes}
}

func (c *Writer) Header() http.Header { return c.w.Header() }

func (c *Writer) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}
	c.statusCode = code
	c.wroteHeader = true
	sse.PrepareJSONResponse(c.w)
	c.w.WriteHeader(code)
}

func (c *Writer) Write(p []byte) (int, error) {
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

func (c *Writer) Flush() {
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *Writer) Captured() ([]byte, int, bool) {
	if c.overflow {
		return nil, 0, false
	}
	out := make([]byte, c.body.Len())
	copy(out, c.body.Bytes())
	return out, c.statusCode, true
}
