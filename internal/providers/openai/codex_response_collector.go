package openai

import (
	"bytes"
	"errors"
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

var errCodexStreamWithoutTerminal = errors.New("Codex stream ended without a terminal response")

// codexResponseCollector serves a non-streaming Responses caller from the Codex
// backend, which only streams: it consumes the SSE events and writes the
// terminal response object as one JSON body.
type codexResponseCollector struct {
	w        http.ResponseWriter
	status   int
	pending  []byte
	items    translate.ResponsesItemSnapshot
	response []byte
	sawEvent bool
}

func newCodexResponseCollector(w http.ResponseWriter) *codexResponseCollector {
	return &codexResponseCollector{w: w, status: http.StatusOK}
}

func (c *codexResponseCollector) Header() http.Header { return c.w.Header() }

func (c *codexResponseCollector) WriteHeader(code int) { c.status = code }

func (c *codexResponseCollector) Write(p []byte) (int, error) {
	c.pending = append(c.pending, p...)
	for {
		event, n := sse.SplitNext(c.pending)
		if n == 0 {
			return len(p), nil
		}
		c.observe(event)
		c.pending = c.pending[n:]
	}
}

func (c *codexResponseCollector) observe(event []byte) {
	_, payload := sse.ParseEvent(event)
	if !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	c.sawEvent = true
	c.items.Observe(root)
	switch root.Get("type").Str {
	case "response.completed", "response.incomplete":
		c.response = []byte(root.Get("response").Raw)
	}
}

// finish writes the collected terminal response, filling an empty output from
// the streamed items.
func (c *codexResponseCollector) finish() error {
	if tail := bytes.TrimSpace(c.pending); len(tail) > 0 {
		if !c.sawEvent && tail[0] == '{' && gjson.ValidBytes(tail) {
			// An upstream that already answered with one JSON body.
			c.response = bytes.Clone(tail)
		} else {
			c.observe(tail)
		}
		c.pending = nil
	}
	if len(c.response) == 0 {
		return errCodexStreamWithoutTerminal
	}
	body, _ := c.items.Backfill(c.response, "output")
	h := c.w.Header()
	h.Set("Content-Type", "application/json")
	h.Del("Content-Length")
	c.w.WriteHeader(c.status)
	_, err := c.w.Write(body)
	return err
}
