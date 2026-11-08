package proxy

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const routerResponseHeaderPrefix = "X-Router-"

var blindExperimentModelPaths = []string{"model", "message.model", "response.model"}

// blindExperimentWriter keeps blind experiment subjects from seeing which model served a turn:
// router headers are dropped and response model fields report the model the client asked for.
type blindExperimentWriter struct {
	http.ResponseWriter
	model       string
	wroteHeader bool
	stream      bool
	passthrough bool
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
	bw := &blindExperimentWriter{ResponseWriter: w, model: gjson.GetBytes(body, "model").String()}
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
	if !b.stream && b.model != "" {
		h.Del("Content-Length")
	}
	b.ResponseWriter.WriteHeader(code)
}

func (b *blindExperimentWriter) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	if b.model == "" || b.passthrough {
		return b.ResponseWriter.Write(p)
	}
	b.pending = append(b.pending, p...)
	if !b.stream {
		return len(p), nil
	}
	end := bytes.LastIndex(b.pending, []byte("\n\n"))
	if end < 0 {
		return len(p), nil
	}
	ready := b.pending[:end+2]
	if _, err := b.ResponseWriter.Write(rewriteSSEModel(ready, b.model)); err != nil {
		return 0, err
	}
	b.pending = append(b.pending[:0], b.pending[end+2:]...)
	return len(p), nil
}

func (b *blindExperimentWriter) Flush() {
	if !b.stream && len(b.pending) > 0 {
		b.release()
		b.passthrough = true
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

func rewriteSSEModel(events []byte, model string) []byte {
	if !bytes.Contains(events, []byte(`"model"`)) {
		return events
	}
	lines := bytes.Split(events, []byte("\n"))
	for i, line := range lines {
		payload, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			continue
		}
		trimmed := bytes.TrimLeft(payload, " ")
		rewritten := rewriteJSONModel(trimmed, model)
		if !bytes.Equal(rewritten, trimmed) {
			lines[i] = append([]byte("data: "), rewritten...)
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
		if current.Type != gjson.String || current.String() == model {
			continue
		}
		if out, err := sjson.SetBytes(doc, path, model); err == nil {
			doc = out
		}
	}
	return doc
}
