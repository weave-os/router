package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const overflowInstallationID = "00000000-0000-0000-0000-000000000001"

func overflowingProvider(body string) *fakeProvider {
	return &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(body)}}
}

// An upstream overflow must reach Claude Code as its native prompt-too-long,
// never the upstream's own body: the handler renders the classified error.
func TestProxyMessages_UpstreamOverflowIsLeftForNativeRendering(t *testing.T) {
	upstream := overflowingProvider(`{"error":{"message":"Your input exceeds the context window of this model.","code":"context_length_exceeded"}}`)
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5", Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderAnthropic: upstream},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"claude-haiku-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	err := svc.ProxyMessages(authedCtx(overflowInstallationID), body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, cls.Kind)
	assert.Len(t, upstream.proxyBodies, 1, "an overflow is not retried against the same window")
	assert.Zero(t, rec.Body.Len(), "the upstream's OpenAI-shaped body must not reach an Anthropic client")
}

// The pre-filter's ÷4 byte estimate counts JSON escape syntax, so an escaped
// prompt can look too large for a forced model it fits. The force must still
// dispatch and leave the verdict to the provider's exact token count; a real
// overflow then returns as the native prompt-too-long error tested above.
func TestProxyMessages_ForcedModelDispatchesPastByteEstimate(t *testing.T) {
	const forced = "claude-sonnet-4-5"
	upstream := &fakeProvider{}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-5", Reason: "cluster"}}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: upstream},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithAvailableModels(map[string]struct{}{forced: {}, "claude-opus-5": {}})

	// 150k non-ASCII runes, each ASCII-escaped to a 6-byte JSON escape: ~900KB
	// on the wire, which the ÷4 estimate reads as ~225k tokens against the
	// forced model's 200k window.
	content := strconv.QuoteToASCII(strings.Repeat("中", 150_000))
	body := []byte(`{"model":"` + forced + `","max_tokens":1024,"messages":[{"role":"user","content":` + content + `}]}`)
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	httpReq.Header.Set(proxy.ForceModelHeader, forced)
	require.NoError(t, svc.ProxyMessages(authedCtx(overflowInstallationID), body, httptest.NewRecorder(), httpReq))

	require.Len(t, upstream.proxyBodies, 1)
	assert.Equal(t, forced, gjson.GetBytes(upstream.proxyBodies[0], "model").String(),
		"the byte estimate alone must not reroute a forced pin")
	assert.Zero(t, fr.routeCalls, "a forced pin never consults the scorer")
}

func proxyResponsesOverflow(t *testing.T, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	upstream := overflowingProvider(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1050000 tokens > 1000000 maximum"}}`)
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5", Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderAnthropic: upstream},
		nil, false, nil, newFakePinStore(), false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
	rec := httptest.NewRecorder()
	body := `{"model":"claude-haiku-4-5","input":"hi","stream":false}`
	if stream {
		body = `{"model":"claude-haiku-4-5","input":"hi","stream":true}`
	}
	err := svc.ProxyOpenAIResponses(authedCtx(overflowInstallationID), []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, cls.Kind)
	return rec
}

// Codex compacts only on an in-stream response.failed carrying
// context_length_exceeded; opencode classifies the same shape.
func TestProxyOpenAIResponses_StreamingOverflowIsNativeResponseFailed(t *testing.T) {
	rec := proxyResponsesOverflow(t, true)
	assert.Equal(t, http.StatusOK, rec.Code)
	out := rec.Body.String()
	assert.Contains(t, out, "event: response.created")
	assert.Contains(t, out, "event: response.failed")
	assert.Contains(t, out, `"code":"context_length_exceeded"`)
	assert.NotContains(t, out, "1050000 tokens", "the upstream's own body never leaks")
}

func TestProxyOpenAIResponses_NonStreamingOverflowIsNativeError(t *testing.T) {
	rec := proxyResponsesOverflow(t, false)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "context_length_exceeded", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	assert.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
}
