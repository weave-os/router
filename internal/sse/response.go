package sse

import (
	"html"
	"io"
	"net/http"
	"strings"
)

var htmlToJSONEscapes = strings.NewReplacer(
	"&lt;", `\u003c`,
	"&gt;", `\u003e`,
	"&amp;", `\u0026`,
	"&#34;", `"`,
	"&#39;", `'`,
	"\u2028", `\u2028`,
	"\u2029", `\u2029`,
)

func PrepareJSONResponse(w http.ResponseWriter) {
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Del("Content-Length")
}

func WriteJSONChunk(w http.ResponseWriter, chunk []byte) (int, error) {
	PrepareJSONResponse(w)
	if w.Header().Get("Content-Type") == "text/event-stream" {
		w.Header().Set("Content-Type", "text/event-stream")
		return w.Write(chunk)
	}
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
