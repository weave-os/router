package sse

import (
	"html"
	"io"
	"net/http"
	"strings"
)

// HTML entities would change JSON values, so re-express escaped HTML
// metacharacters as JSON unicode escapes and restore structural quotes.
var htmlToJSONEscapes = strings.NewReplacer(
	"&lt;", `\u003c`,
	"&gt;", `\u003e`,
	"&amp;", `\u0026`,
	"&#34;", `"`,
	"&#39;", `'`,
	"\u2028", `\u2028`,
	"\u2029", `\u2029`,
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
	escaped := htmlToJSONEscapes.Replace(html.EscapeString(string(chunk)))
	written, err := w.Write([]byte(escaped))
	if err != nil {
		return 0, err
	}
	if written != len(escaped) {
		return 0, io.ErrShortWrite
	}
	return len(chunk), nil
}
