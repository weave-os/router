package observability

import (
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
