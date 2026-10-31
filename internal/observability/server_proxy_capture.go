package observability

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// GitHub's backend sends this user agent when it proxies an organization's
// Copilot custom-model traffic to the router.
const githubCopilotProxyUserAgent = "copilot-api"

var sensitiveHeaderName = regexp.MustCompile(`(?i)authorization|cookie|token|key|secret|signature`)

// LogServerProxyRequestShape logs the headers and top-level body keys of a
// request proxied by GitHub's Copilot backend, so we can see what identity it
// forwards. Credential-bearing header values are replaced with their length.
func LogServerProxyRequestShape(c *gin.Context, body []byte) {
	if c.Request.UserAgent() != githubCopilotProxyUserAgent {
		return
	}
	FromGin(c).Info("Copilot proxy request shape",
		"inbound_headers", redactedHeaders(c.Request.Header),
		"body_keys", topLevelKeys(body),
	)
}

// responseIDScanLimit bounds how much of the response is buffered while
// looking for the first "id" field; the id leads the first SSE chunk.
const responseIDScanLimit = 4096

var responseIDField = []byte(`"id":"`)

// serverProxyResponseRecorder records the response id the router sends back
// to GitHub's proxy. GitHub passes it through unchanged to the client, whose
// OpenTelemetry spans carry it as gen_ai.response.id, so it joins those spans
// (and their user identity) to this request.
type serverProxyResponseRecorder struct {
	gin.ResponseWriter
	head       []byte
	responseID string
	done       bool
}

func (w *serverProxyResponseRecorder) Write(b []byte) (int, error) {
	w.scan(b)
	return w.ResponseWriter.Write(b)
}

func (w *serverProxyResponseRecorder) WriteString(s string) (int, error) {
	w.scan([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

func (w *serverProxyResponseRecorder) scan(b []byte) {
	if w.done {
		return
	}
	w.head = append(w.head, b[:min(len(b), responseIDScanLimit-len(w.head))]...)
	if start := bytes.Index(w.head, responseIDField); start >= 0 {
		rest := w.head[start+len(responseIDField):]
		if end := bytes.IndexByte(rest, '"'); end >= 0 {
			w.responseID = string(rest[:end])
			w.done = true
		}
	}
	if len(w.head) >= responseIDScanLimit {
		w.done = true
	}
	if w.done {
		w.head = nil
	}
}

// RecordServerProxyResponseID wraps the response writer of a request proxied
// by GitHub's Copilot backend and returns a func that logs the response id
// once the handler has written it. For any other client it is a no-op.
func RecordServerProxyResponseID(c *gin.Context) func() {
	if c.Request.UserAgent() != githubCopilotProxyUserAgent {
		return func() {}
	}
	recorder := &serverProxyResponseRecorder{ResponseWriter: c.Writer}
	c.Writer = recorder
	return func() {
		FromGin(c).Info("Copilot proxy response id",
			"response_id", sanitizeLogValue(recorder.responseID),
		)
	}
}

func redactedHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		joined := strings.Join(values, ", ")
		if sensitiveHeaderName.MatchString(name) {
			out[name] = "<redacted len=" + strconv.Itoa(len(joined)) + ">"
			continue
		}
		out[name] = sanitizeLogValue(joined)
	}
	return out
}

func topLevelKeys(body []byte) []string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
