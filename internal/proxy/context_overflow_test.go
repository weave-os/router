package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"

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

// responsesOverflowClient answers the way OpenAI's Responses API does for an
// over-window prompt: HTTP 200, then an in-stream error and response.failed.
type responsesOverflowClient struct {
	endpoints []providers.Endpoint
}

func (c *responsesOverflowClient) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.endpoints = append(c.endpoints, prep.Endpoint)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, payload := range []string{
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]},"sequence_number":0}`,
		`{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":1}`,
		`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."},"output":[]},"sequence_number":2}`,
	} {
		if _, err := io.WriteString(w, "data: "+payload+"\n\n"); err != nil {
			return err
		}
	}
	return nil
}

func (c *responsesOverflowClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// A GPT model reports an overflow inside a 200 stream. Translated for an
// Anthropic or chat client it must still classify as a context overflow, so
// the handler renders the native prompt-too-long rather than a generic 502.
func TestCrossFormatResponsesOverflowClassifiesAsContextWindowExceeded(t *testing.T) {
	ingresses := map[string]struct {
		path  string
		body  string
		proxy func(*proxy.Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		"messages": {
			path: "/v1/messages",
			body: `{"model":"gpt-5-mini","max_tokens":1024,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`,
			proxy: func(s *proxy.Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error {
				return s.ProxyMessages
			},
		},
		"chat_completions": {
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5-mini","max_tokens":1024,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`,
			proxy: func(s *proxy.Service) func(context.Context, []byte, http.ResponseWriter, *http.Request) error {
				return s.ProxyOpenAIChatCompletion
			},
		},
	}
	for name, ingress := range ingresses {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", name, stream), func(t *testing.T) {
				upstream := &responsesOverflowClient{}
				svc := proxy.NewService(
					&fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5-mini", Reason: "cluster"}},
					map[string]providers.Client{providers.ProviderOpenAI: upstream},
					nil, false, nil, newFakePinStore(), false, providers.ProviderOpenAI, "gpt-5-mini", nil,
				)
				rec := httptest.NewRecorder()
				body := []byte(fmt.Sprintf(ingress.body, stream))
				err := ingress.proxy(svc)(authedCtx(overflowInstallationID), body, rec, httptest.NewRequest(http.MethodPost, ingress.path, nil))

				require.Error(t, err)
				cls, ok := proxy.ClassifyDispatchError(err)
				require.True(t, ok)
				assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, cls.Kind)
				require.Equal(t, []providers.Endpoint{providers.EndpointResponses}, upstream.endpoints,
					"served on the Responses API, and an overflow is not retried against the same window")
				assert.NotContains(t, rec.Body.String(), "upstream Responses request failed")
			})
		}
	}
}

// overflowPolicy is a sidecar that always picks the first offered candidate.
type overflowPolicy struct{}

func (overflowPolicy) Decide(_ context.Context, query policy.Query) (policy.Result, error) {
	if len(query.Candidates) == 0 {
		return policy.Result{}, errors.New("no candidates offered")
	}
	return policy.Result{Model: query.Candidates[0].CatalogID, Provider: query.Candidates[0].Provider}, nil
}

// hmmOverflowService routes on the HMM policy path over models, all served by
// provider through upstream.
func hmmOverflowService(provider string, upstream providers.Client, models ...string) *proxy.Service {
	available := make(map[string]struct{}, len(models))
	for _, model := range models {
		available[model] = struct{}{}
	}
	resolver := policy.NewResolver(available, map[string]struct{}{provider: {}}, func(m catalog.Model) string { return m.ID }, policy.ManagedProviderPolicy())
	routing := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM, Unavailable: hmm.ErrHMMUnavailable}, overflowPolicy{}, resolver)
	return proxy.NewService(&fakeRouter{}, map[string]providers.Client{provider: upstream},
		nil, false, nil, newFakePinStore(), false, provider, models[0], nil).
		WithAvailableModels(available).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: routing, Unavailable: hmm.ErrHMMUnavailable})
}

// A request whose estimate rules out every model reaches the widest one on
// the HMM path too, so the provider's exact count decides and a real overflow
// returns as the client's native prompt-too-long, never "no model is routable".
func TestHMMTotalOverflowReachesUpstreamOnEveryIngress(t *testing.T) {
	// ~1.1M tokens of user text: past every window, by the pre-filter's byte
	// estimate and the resolver's text estimate alike.
	prompt := strings.Repeat("overflow ", 500_000)
	for _, test := range []struct {
		name, provider, path, body, admitted string
		models                               []string
		send                                 func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", providers.ProviderAnthropic, "/v1/messages",
			`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"` + prompt + `"}]}`,
			"claude-opus-4-8", []string{"claude-opus-4-8", "claude-haiku-4-5"}, (*proxy.Service).ProxyMessages},
		{"chat", providers.ProviderAnthropic, "/v1/chat/completions",
			`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"` + prompt + `"}]}`,
			"claude-opus-4-8", []string{"claude-opus-4-8", "claude-haiku-4-5"}, (*proxy.Service).ProxyOpenAIChatCompletion},
		{"responses", providers.ProviderAnthropic, "/v1/responses",
			`{"model":"claude-opus-4-8","input":"` + prompt + `"}`,
			"claude-opus-4-8", []string{"claude-opus-4-8", "claude-haiku-4-5"}, (*proxy.Service).ProxyOpenAIResponses},
		{"gemini", providers.ProviderGoogle, "/v1beta/models/gemini-2.5-pro:generateContent",
			`{"contents":[{"role":"user","parts":[{"text":"` + prompt + `"}]}]}`,
			"gemini-2.5-pro", []string{"gemini-2.5-pro"}, (*proxy.Service).ProxyGeminiGenerateContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := overflowingProvider(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1100000 tokens > 1000000 maximum"}}`)
			svc := hmmOverflowService(test.provider, upstream, test.models...)
			ctx := router.WithStrategy(authedCtx(overflowInstallationID), router.StrategyHMM)

			err := test.send(svc, ctx, []byte(test.body), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, test.path, nil))

			require.Error(t, err)
			cls, ok := proxy.ClassifyDispatchError(err)
			require.True(t, ok)
			assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, cls.Kind, "got %v", err)
			require.Len(t, upstream.proxyBodies, 1, "the widest model is dispatched so the provider decides")
		})
	}
}

// A wider-window model the request cannot reach (its provider is not enabled)
// must not mask a total overflow of every servable model: the widest servable
// one is still dispatched so the provider decides.
func TestTotalOverflowIgnoresWiderUnservableModel(t *testing.T) {
	// ~900k-token estimate plus a 128k output reserve: past a 1M window, inside
	// Gemini's 1,048,576 one.
	prompt := strings.Repeat("overflow ", 400_000)
	body := `{"model":"claude-opus-4-8","max_tokens":128000,"messages":[{"role":"user","content":"` + prompt + `"}]}`
	upstream := overflowingProvider(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1100000 tokens > 1000000 maximum"}}`)
	svc := hmmOverflowService(providers.ProviderAnthropic, upstream, "claude-opus-4-8", "gemini-3.5-flash")
	ctx := router.WithStrategy(authedCtx(overflowInstallationID), router.StrategyHMM)

	err := svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorContextWindowExceeded, cls.Kind, "got %v", err)
	require.Len(t, upstream.proxyBodies, 1, "the widest servable model is dispatched so the provider decides")
}

// When a policy reason, not size, empties the pool, the org still hears it is
// its model selection to widen.
func TestHMMPolicyEmptiedPoolStaysNoRoutableModels(t *testing.T) {
	upstream := &fakeProvider{}
	svc := hmmOverflowService(providers.ProviderAnthropic, upstream, "claude-opus-4-8", "claude-haiku-4-5")
	ctx := context.WithValue(router.WithStrategy(authedCtx(overflowInstallationID), router.StrategyHMM),
		proxy.InstallationExcludedModelsContextKey{}, []string{"claude-opus-4-8", "claude-haiku-4-5"})
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

	err := svc.ProxyMessages(ctx, body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	require.Error(t, err)
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorNoRoutableModels, cls.Kind, "got %v", err)
	assert.Empty(t, upstream.proxyBodies)
}
