package observability

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
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
// looking for "id" fields; ids lead the first SSE chunks.
const responseIDScanLimit = 4096

// maxResponseIDs caps how many distinct ids are recorded. A prepended routing
// marker chunk carries its own id ahead of the completion's, so the first id
// alone is not necessarily the one the client reports.
const maxResponseIDs = 4

var responseIDField = regexp.MustCompile(`"id"\s*:\s*"([^"]*)"`)

// serverProxyResponseRecorder records the response ids the router sends back
// to GitHub's proxy. GitHub passes them through unchanged to the client, whose
// OpenTelemetry spans carry one as gen_ai.response.id, so they join those
// spans (and their user identity) to this request.
type serverProxyResponseRecorder struct {
	gin.ResponseWriter
	head        []byte
	responseIDs []string
	done        bool
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
	w.responseIDs = w.responseIDs[:0]
	for _, match := range responseIDField.FindAllSubmatch(w.head, -1) {
		id := sanitizeLogValue(string(match[1]))
		if !slices.Contains(w.responseIDs, id) {
			w.responseIDs = append(w.responseIDs, id)
		}
		if len(w.responseIDs) == maxResponseIDs {
			break
		}
	}
	if len(w.head) >= responseIDScanLimit || len(w.responseIDs) == maxResponseIDs {
		w.done = true
		w.head = nil
	}
}

// RecordServerProxyResponseID wraps the response writer of a request proxied
// by GitHub's Copilot backend and returns a func that logs the response ids
// once the handler has written them. For any other client it is a no-op.
func RecordServerProxyResponseID(c *gin.Context) func() {
	if c.Request.UserAgent() != githubCopilotProxyUserAgent {
		return func() {}
	}
	recorder := &serverProxyResponseRecorder{ResponseWriter: c.Writer}
	c.Writer = recorder
	return func() {
		FromGin(c).Info("Copilot proxy response id",
			"response_ids", recorder.responseIDs,
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
