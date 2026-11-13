package openaicompat_test

// Verify that Grok models use the wider ResponseHeaderTimeout transport
// and non-Grok models keep the default.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func slowHeaderUpstream(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-slow","object":"chat.completion"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProxy_GrokModelGetsWiderHeaderTimeout(t *testing.T) {
	upstream := slowHeaderUpstream(t, 300*time.Millisecond)
	c := openaicompat.NewClientWithHeaderTimeouts("test-key", upstream.URL+"/v1", 50*time.Millisecond, 5*time.Second)

	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: []byte(`{"model":"grok-4.6","messages":[]}`), Headers: make(http.Header)}
	err := c.Proxy(context.Background(), router.Decision{Model: "grok-4.6"}, prep, rec, clientReq)

	require.NoError(t, err, "a grok turn must ride the wider first-byte guard, not the default one")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestProxy_NonGrokModelKeepsDefaultHeaderTimeout(t *testing.T) {
	upstream := slowHeaderUpstream(t, 300*time.Millisecond)
	c := openaicompat.NewClientWithHeaderTimeouts("test-key", upstream.URL+"/v1", 50*time.Millisecond, 5*time.Second)

	rec := httptest.NewRecorder()
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	prep := providers.PreparedRequest{Body: []byte(`{"model":"gpt-5.6","messages":[]}`), Headers: make(http.Header)}
	err := c.Proxy(context.Background(), router.Decision{Model: "gpt-5.6"}, prep, rec, clientReq)

	require.Error(t, err, "a non-grok turn must stay on the default first-byte guard")
	assert.Contains(t, err.Error(), "timeout awaiting response headers")
}
