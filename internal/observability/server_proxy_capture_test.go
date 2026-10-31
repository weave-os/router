package observability_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func serveServerProxyShape(t *testing.T, userAgent string, headers map[string]string, body string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var buf strings.Builder
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	engine := gin.New()
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		observability.LogServerProxyRequestShape(c, []byte(body))
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	engine.ServeHTTP(httptest.NewRecorder(), req)
	return buf.String()
}

func TestLogServerProxyRequestShapeRedactsCredentialsAndListsBodyKeys(t *testing.T) {
	out := serveServerProxyShape(t, "copilot-api", map[string]string{
		"Authorization":       "Bearer rk_secret",
		"X-Client-Session-Id": "6a325047-683a-4d1a-8f3a-b712352dad05",
		"X-Weave-Router-Key":  "rk_other",
	}, `{"model":"auto","user":"octocat","messages":[]}`)

	assert.NotContains(t, out, "rk_secret")
	assert.NotContains(t, out, "rk_other")
	line := findLogLine(t, out, "Copilot proxy request shape")
	headers := line["inbound_headers"].(map[string]any)
	assert.Equal(t, "<redacted len=16>", headers["Authorization"])
	assert.Equal(t, "<redacted len=8>", headers["X-Weave-Router-Key"])
	assert.Equal(t, "6a325047-683a-4d1a-8f3a-b712352dad05", headers["X-Client-Session-Id"])
	assert.Equal(t, []any{"messages", "model", "user"}, line["body_keys"])
}

func TestLogServerProxyRequestShapeSkipsOtherClients(t *testing.T) {
	out := serveServerProxyShape(t, "copilot/1.0.105 (linux v24.22.1)", nil, `{"model":"auto"}`)
	assert.NotContains(t, out, "Copilot proxy request shape")
}

func serveServerProxyResponse(t *testing.T, userAgent string, chunks ...string) (string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var buf strings.Builder
	restore := swapDefaultLogger(t, &buf)
	defer restore()

	engine := gin.New()
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		defer observability.RecordServerProxyResponseID(c)()
		for _, chunk := range chunks {
			_, _ = c.Writer.WriteString(chunk)
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("User-Agent", userAgent)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return buf.String(), rec.Body.String()
}

func TestRecordServerProxyResponseIDLogsMarkerAndCompletionIDs(t *testing.T) {
	chunks := []string{
		`data: {"id":"chatcmpl-marker","object":"chat.completion.chunk"}` + "\n\n",
		`data: {"id":"chatcmpl-01`, `7a2be23e778731","object":"chat.completion.chunk"}` + "\n\n",
		`data: {"id":"chatcmpl-017a2be23e778731"}` + "\n\n",
	}
	out, body := serveServerProxyResponse(t, "copilot-api", chunks...)

	assert.Equal(t, strings.Join(chunks, ""), body)
	line := findLogLine(t, out, "Copilot proxy response id")
	assert.Equal(t, []any{"chatcmpl-marker", "chatcmpl-017a2be23e778731"}, line["response_ids"])
}

func TestRecordServerProxyResponseIDAllowsJSONWhitespace(t *testing.T) {
	out, _ := serveServerProxyResponse(t, "copilot-api", `{"id" : "resp_123", "object": "response"}`)

	line := findLogLine(t, out, "Copilot proxy response id")
	assert.Equal(t, []any{"resp_123"}, line["response_ids"])
}

func TestRecordServerProxyResponseIDIgnoresOtherClients(t *testing.T) {
	out, body := serveServerProxyResponse(t, "claude-cli/2.0", `data: {"id":"chatcmpl-x"}`)

	assert.Equal(t, `data: {"id":"chatcmpl-x"}`, body)
	assert.NotContains(t, out, "Copilot proxy response id")
}
