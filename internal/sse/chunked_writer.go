package sse

import (
	"bufio"
	"net/http"
	"strings"
)

type ChunkedWriter struct {
	Inner   http.ResponseWriter
	Flusher http.Flusher
	BW      *bufio.Writer

	Streaming      bool
	HeadersEmitted bool
}

func NewChunkedWriter(w http.ResponseWriter, bufSize int) ChunkedWriter {
	flusher, _ := w.(http.Flusher)
	return ChunkedWriter{
		Inner:   w,
		Flusher: flusher,
		BW:      bufio.NewWriterSize(w, bufSize),
	}
}

func (c *ChunkedWriter) Header() http.Header {
	return c.Inner.Header()
}

func (c *ChunkedWriter) WriteHeader(code int) {
	if c.HeadersEmitted {
		return
	}
	ct := c.Inner.Header().Get("Content-Type")
	c.Streaming = strings.Contains(ct, "text/event-stream") && code < 400
	c.HeadersEmitted = true
	PrepareJSONResponse(c.Inner)
	c.Inner.WriteHeader(code)
}

func (c *ChunkedWriter) Flush() {
	if c.Flusher != nil {
		c.Flusher.Flush()
	}
}

func (c *ChunkedWriter) FlushEvent() error {
	return FlushWriter(c.BW, c.Flusher)
}

func FlushWriter(bw *bufio.Writer, f http.Flusher) error {
	if err := bw.Flush(); err != nil {
		return err
	}
	if f != nil {
		f.Flush()
	}
	return nil
}
