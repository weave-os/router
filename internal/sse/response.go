package sse

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// PrepareJSONResponse must run before headers commit: escaping expands bytes,
// and provider-controlled media types must never turn API output into HTML.
func PrepareJSONResponse(w http.ResponseWriter) {
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Del("Content-Length")
}

// WriteJSONChunk forwards JSON or SSE bytes with HTML-safe JSON escaping.
// It reports consumed input bytes, since escaping can expand the wire bytes.
func WriteJSONChunk(w http.ResponseWriter, chunk []byte) (int, error) {
	PrepareJSONResponse(w)
	var escaped bytes.Buffer
	json.HTMLEscape(&escaped, chunk)
	written, err := w.Write(escaped.Bytes())
	if err != nil {
		return 0, err
	}
	if written != escaped.Len() {
		return 0, io.ErrShortWrite
	}
	return len(chunk), nil
}
