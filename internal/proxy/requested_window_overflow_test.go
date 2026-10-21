package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

const requestedWindowModel = "claude-opus-5-5"

// pastRequestedWindowPrompt estimates ~1.02M tokens: past the requested
// model's 1M window, inside gpt-6-luna's 1.05M one.
var pastRequestedWindowPrompt = strings.Repeat("overflow ", 453_000)

func proxyRequestedWindowOverflow(t *testing.T, prompt string, stream bool, upstreamErr error) (*httptest.ResponseRecorder, error, *fakeProvider) {
	t.Helper()
	upstream := &fakeProvider{proxyErr: upstreamErr}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna, Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderOpenAI: upstream},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAnthropic, requestedWindowModel, nil,
	).WithAvailableModels(map[string]struct{}{catalog.ModelGPT6Luna: {}, requestedWindowModel: {}})
	streamField := "false"
	if stream {
		streamField = "true"
	}
	body := []byte(`{"model":"` + requestedWindowModel + `","max_tokens":1024,"stream":` + streamField + `,"messages":[{"role":"user","content":"` + prompt + `"}]}`)
	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(authedCtx(overflowInstallationID), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	return rec, err, upstream
}

func requireDispatchKind(t *testing.T, err error) proxy.DispatchErrorKind {
	t.Helper()
	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok, "unclassified: %v", err)
	return cls.Kind
}

// A history the client's own model can no longer hold, served on a
// larger-window model that fails before output, must reach Claude Code as its
// native prompt-too-long. A plain 5xx makes the client resend the same
// oversized history instead of compacting.
func TestProxyMessages_PreOutputFailurePastRequestedWindowAsksClientToCompact(t *testing.T) {
	for _, stream := range []bool{false, true} {
		rec, err, upstream := proxyRequestedWindowOverflow(t, pastRequestedWindowPrompt, stream, &providers.UpstreamErrorResponse{Status: http.StatusBadGateway})

		assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, requireDispatchKind(t, err), "stream=%v: %v", stream, err)
		assert.NotEmpty(t, upstream.proxyBodies, "stream=%v: the larger-window model is still tried", stream)
		assert.NotEqual(t, http.StatusBadGateway, rec.Code, "stream=%v: the upstream 5xx must not reach the client", stream)
		if out := rec.Body.String(); out != "" {
			assert.Contains(t, out, "prompt is too long", "stream=%v: an in-stream frame carries the native overflow", stream)
		}
	}
}

// Rate limits are a throttle, not a size signal: compaction would not fix them.
func TestProxyMessages_RateLimitPastRequestedWindowIsNotOverflow(t *testing.T) {
	_, err, _ := proxyRequestedWindowOverflow(t, pastRequestedWindowPrompt, false, &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests})

	assert.NotEqual(t, proxy.DispatchErrorContextWindowExceeded, requireDispatchKind(t, err))
}

// While the history still fits the requested model, the client's own budget is
// accurate and a 5xx is transient; compacting would discard context for nothing.
func TestProxyMessages_PreOutputFailureWithinRequestedWindowIsNotOverflow(t *testing.T) {
	_, err, _ := proxyRequestedWindowOverflow(t, "hi", false, &providers.UpstreamErrorResponse{Status: http.StatusBadGateway})

	assert.NotEqual(t, proxy.DispatchErrorContextWindowExceeded, requireDispatchKind(t, err))
}
