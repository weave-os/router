package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxyMessages_KeepaliveDuringUpstreamSilence verifies a committed stream
// silent during reasoning is padded so the client byte watchdog doesn't abort it.
func TestProxyMessages_KeepaliveDuringUpstreamSilence(t *testing.T) {
	const upstreamSilence = 250 * time.Millisecond

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		write := func(chunk string) {
			_, _ = w.Write([]byte(chunk))
			if flusher != nil {
				flusher.Flush()
			}
		}

		write(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"thinking"},"finish_reason":null}]}` + "\n\n")
		// The stall: committed, then nothing the translator can forward.
		time.Sleep(upstreamSilence)
		write(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{"content":" done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}` + "\n\n")
		write("data: [DONE]\n\n")
	}))
	defer upstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: "fireworks", Model: "deepseek/deepseek-v4-pro"}},
		map[string]providers.Client{"fireworks": openaicompat.NewClient("test-fw-key", upstream.URL)},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{"fireworks": {}}).
		WithSSEKeepalive(40 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"deepseek/deepseek-v4-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))

	got := rec.Body.String()
	assert.GreaterOrEqual(t, strings.Count(got, "event: ping"), 2,
		"a stalled-but-live stream must be padded with pings")

	// The padding must not break the turn: real frames still bracket the stream,
	// the answer survives, and no ping precedes message_start.
	assert.Contains(t, got, "event: message_start")
	assert.Contains(t, got, "event: message_stop")
	assert.Contains(t, got, "thinking")
	assert.Contains(t, got, " done")
	assert.Less(t, strings.Index(got, "event: message_start"), strings.Index(got, "event: ping"),
		"keepalives must never precede the stream envelope")
}

// The keepalive is a kill-switchable addition: with it off, the stream is
// byte-for-byte what it was before, so an operator can always turn it back off.
func TestProxyMessages_KeepaliveDisabledLeavesStreamUnpadded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		write := func(chunk string) {
			_, _ = w.Write([]byte(chunk))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n")
		time.Sleep(150 * time.Millisecond)
		write(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}` + "\n\n")
		write("data: [DONE]\n\n")
	}))
	defer upstream.Close()

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: "fireworks", Model: "deepseek/deepseek-v4-pro"}},
		map[string]providers.Client{"fireworks": openaicompat.NewClient("test-fw-key", upstream.URL)},
		nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{"fireworks": {}}).
		WithSSEKeepalive(0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"deepseek/deepseek-v4-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	require.NoError(t, svc.ProxyMessages(context.Background(), body, rec, req))

	assert.NotContains(t, rec.Body.String(), "event: ping", "keepalives must be off when the interval is 0")
	assert.Contains(t, rec.Body.String(), "event: message_stop")
}
