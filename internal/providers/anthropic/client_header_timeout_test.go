package anthropic_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowPrefillUpstream mimics Anthropic holding response headers until prefill
// completes, even for a streaming request.
func slowPrefillUpstream(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_slow"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func messagesBody(promptBytes int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"model":"claude-opus-5-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"`)
	b.WriteString(strings.Repeat("a", promptBytes))
	b.WriteString(`"}]}`)
	return b.Bytes()
}

func TestProxy_LargePromptSurvivesSlowPrefill(t *testing.T) {
	upstream := slowPrefillUpstream(t, 300*time.Millisecond)
	c := anthropic.NewClientWithHeaderTimeouts("test-key", upstream.URL, 50*time.Millisecond, 5*time.Second)

	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: messagesBody(2 << 20), Headers: make(http.Header)}
	err := c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5-5"}, prep, rec, clientReq)

	require.NoError(t, err, "a near-window prompt must ride the wider first-byte guard while Anthropic prefills")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"id":"msg_slow"`)
}

func TestProxy_SmallPromptKeepsDefaultHeaderTimeout(t *testing.T) {
	upstream := slowPrefillUpstream(t, 300*time.Millisecond)
	c := anthropic.NewClientWithHeaderTimeouts("test-key", upstream.URL, 50*time.Millisecond, 5*time.Second)

	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: messagesBody(1024), Headers: make(http.Header)}
	err := c.Proxy(context.Background(), router.Decision{Model: "claude-opus-5-5"}, prep, rec, clientReq)

	require.Error(t, err, "a small prompt must still fail fast on the default first-byte guard")
	assert.Contains(t, err.Error(), "timeout awaiting response headers")
	assert.True(t, providers.IsRetryable(err), "a small-prompt header timeout stays a failover-eligible upstream fault")
}

func TestPassthrough_LargePromptSurvivesSlowPrefill(t *testing.T) {
	upstream := slowPrefillUpstream(t, 300*time.Millisecond)
	c := anthropic.NewClientWithHeaderTimeouts("test-key", upstream.URL, 50*time.Millisecond, 5*time.Second)

	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: messagesBody(2 << 20), Headers: make(http.Header)}
	err := c.Passthrough(context.Background(), prep, rec, clientReq)

	require.NoError(t, err, "passthrough must select the first-byte guard by body size like Proxy does")
	assert.Equal(t, http.StatusOK, rec.Code)
}
