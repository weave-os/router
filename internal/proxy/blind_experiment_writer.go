package proxy

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const routerResponseHeaderPrefix = "X-Router-"

var clientModelIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:/@\[\]-]{1,200}$`)

var blindExperimentModelPaths = []string{"model", "message.model", "response.model", "modelVersion"}

var blindExperimentCostPaths = []string{"usage.weave_cost", "message.usage.weave_cost", "response.usage.weave_cost"}

// blindExperimentWriter keeps blind experiment subjects from seeing which model served a turn:
// router headers are dropped and response model fields report the model the client asked for.
type blindExperimentWriter struct {
	http.ResponseWriter
	model       string
	wroteHeader bool
	stream      bool
	pending     []byte
}

// withBlindExperimentWriter wraps w for active blind experiment subjects. The returned finish
// must run after the last write so a buffered JSON body is released.
func withBlindExperimentWriter(ctx context.Context, w http.ResponseWriter, body []byte) (http.ResponseWriter, func()) {
	if _, active := auth.BlindExperimentFrom(ctx); !active {
		return w, func() {}
	}
	switch w.(type) {
	case *blindExperimentWriter, *translate.ResponsesWriter:
		return w, func() {}
	}
	bw := &blindExperimentWriter{ResponseWriter: w}
	if requested := gjson.GetBytes(body, "model").String(); clientModelIDPattern.MatchString(requested) {
		bw.model = requested
	}
	return bw, bw.finish
}

func (b *blindExperimentWriter) Unwrap() http.ResponseWriter { return b.ResponseWriter }

func (b *blindExperimentWriter) WriteHeader(code int) {
	if b.wroteHeader {
		return
	}
	b.wroteHeader = true
	h := b.ResponseWriter.Header()
	stripRouterResponseHeaders(h)
	b.stream = strings.Contains(h.Get("Content-Type"), "text/event-stream")
	if !b.stream {
		h.Del("Content-Length")
	}
	b.ResponseWriter.WriteHeader(code)
}

func (b *blindExperimentWriter) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	b.pending = append(b.pending, p...)
	if !b.stream {
		return len(p), nil
	}
	end := sseRecordEnd(b.pending)
	if end < 0 {
		return len(p), nil
	}
	ready := b.pending[:end]
	if _, err := b.ResponseWriter.Write(rewriteSSEModel(ready, b.model)); err != nil {
		return 0, err
	}
	b.pending = append(b.pending[:0], b.pending[end:]...)
	return len(p), nil
}

func (b *blindExperimentWriter) Flush() {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	if !b.stream {
		return
	}
	if f, ok := b.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (b *blindExperimentWriter) finish() {
	if !b.wroteHeader {
		stripRouterResponseHeaders(b.ResponseWriter.Header())
		return
	}
	b.release()
}

func (b *blindExperimentWriter) release() {
	if len(b.pending) == 0 {
		return
	}
	out := b.pending
	if b.stream {
		out = rewriteSSEModel(out, b.model)
	} else {
		out = rewriteJSONModel(out, b.model)
	}
	b.pending = nil
	_, _ = b.ResponseWriter.Write(out)
}

func stripRouterResponseHeaders(h http.Header) {
	h.Del("Access-Control-Expose-Headers")
	for name := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), routerResponseHeaderPrefix) {
			delete(h, name)
		}
	}
}

// sseRecordEnd returns the offset just past the last complete SSE record, or -1.
func sseRecordEnd(buf []byte) int {
	end := -1
	if i := bytes.LastIndex(buf, []byte("\n\n")); i >= 0 {
		end = i + 2
	}
	if i := bytes.LastIndex(buf, []byte("\r\n\r\n")); i >= 0 && i+4 > end {
		end = i + 4
	}
	return end
}

func rewriteSSEModel(events []byte, model string) []byte {
	if !bytes.Contains(events, []byte(`"model"`)) && !bytes.Contains(events, []byte(`"weave_cost"`)) {
		return events
	}
	lines := bytes.Split(events, []byte("\n"))
	for i, line := range lines {
		payload, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			continue
		}
		trimmed := bytes.TrimLeft(payload, " ")
		hadCR := bytes.HasSuffix(trimmed, []byte("\r"))
		trimmed = bytes.TrimSuffix(trimmed, []byte("\r"))
		rewritten := rewriteJSONModel(trimmed, model)
		if !bytes.Equal(rewritten, trimmed) {
			lines[i] = append([]byte("data: "), rewritten...)
			if hadCR {
				lines[i] = append(lines[i], '\r')
			}
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

func rewriteJSONModel(doc []byte, model string) []byte {
	if !gjson.ValidBytes(doc) {
		return doc
	}
	for _, path := range blindExperimentModelPaths {
		current := gjson.GetBytes(doc, path)
		if model == "" || current.Type != gjson.String || current.String() == model {
			continue
		}
		if out, err := sjson.SetBytes(doc, path, model); err == nil {
			doc = out
		}
	}
	for _, path := range blindExperimentCostPaths {
		if out, err := sjson.DeleteBytes(doc, path); err == nil {
			doc = out
		}
	}
	return doc
}
