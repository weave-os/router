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
	"weave-os/router/internal/timing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

func attrInt(sp *tracev1.Span, key string) (int64, bool) {
	for _, kv := range sp.Attributes {
		if kv.Key != key {
			continue
		}
		if iv, ok := kv.Value.Value.(*commonv1.AnyValue_IntValue); ok {
			return iv.IntValue, true
		}
	}
	return 0, false
}

// TestUpstreamSpan_FirstOutputMs_ExceedsTTFTOnReasoningStall verifies that
// a role-only keepalive does not satisfy first_output_ms.
func TestUpstreamSpan_FirstOutputMs_ExceedsTTFTOnReasoningStall(t *testing.T) {
	const preOutputStall = 120 * time.Millisecond

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
		// Byte-alive immediately, but role-only: nothing the client can render.
		write(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n")
		time.Sleep(preOutputStall)
		write(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek/deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"finally"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}` + "\n\n")
		write("data: [DONE]\n\n")
	}))
	defer upstream.Close()

	collector := newSpanCollector(t)
	emitter := newTestEmitter(t, collector.srv.URL)

	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: "fireworks", Model: "deepseek/deepseek-v4-pro"}},
		map[string]providers.Client{"fireworks": openaicompat.NewClient("test-fw-key", upstream.URL)},
		emitter, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(map[string]struct{}{"fireworks": {}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"deepseek/deepseek-v4-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	// Middleware installs Timing on the request context in prod; without it
	// there are no latency attributes at all.
	ctx, _ := timing.WithTiming(context.Background())
	require.NoError(t, svc.ProxyMessages(ctx, body, rec, req.WithContext(ctx)))
	require.NoError(t, emitter.Shutdown(context.Background()))

	collector.mu.Lock()
	spans := collector.byName["router.upstream"]
	collector.mu.Unlock()
	require.Len(t, spans, 1, "exactly one router.upstream span must be exported")
	sp := spans[0]

	firstByte, hasFirstByte := attrInt(sp, "latency.upstream_first_byte_ms")
	firstOutput, hasFirstOutput := attrInt(sp, "latency.first_output_ms")
	require.True(t, hasFirstByte, "the established first-byte attribute must stay present")
	require.True(t, hasFirstOutput, "a turn that produced output must carry latency.first_output_ms")

	assert.GreaterOrEqual(t, firstOutput, int64(preOutputStall/time.Millisecond),
		"first output must be measured past the stall, not at the role-only frame")
	assert.Greater(t, firstOutput, firstByte,
		"the whole point: a byte-alive stream with no renderable content must not look healthy")
}
