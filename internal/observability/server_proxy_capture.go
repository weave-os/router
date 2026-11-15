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

const githubCopilotProxyUserAgent = "copilot-api"

var sensitiveHeaderName = regexp.MustCompile(`(?i)authorization|cookie|token|key|secret|signature`)

func LogServerProxyRequestShape(c *gin.Context, body []byte) {
	if c.Request.UserAgent() != githubCopilotProxyUserAgent {
		return
	}
	FromGin(c).Info("Copilot proxy request shape",
		"inbound_headers", redactedHeaders(c.Request.Header),
		"body_keys", topLevelKeys(body),
	)
}

const responseIDScanLimit = 4096

const maxResponseIDs = 4

var responseIDField = regexp.MustCompile(`"id"\s*:\s*"([^"]*)"`)

type serverProxyResponseRecorder struct {
	gin.ResponseWriter
	head        []byte
	responseIDs []string
	done        bool
}

func (w *serverProxyResponseRecorder) Write(b []byte) (int, error) {
	w.scan(b)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return w.ResponseWriter.Write(b)
}

func (w *serverProxyResponseRecorder) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
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
