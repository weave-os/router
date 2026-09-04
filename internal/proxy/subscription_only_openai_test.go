package proxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Codex (ChatGPT) subscription bearer is a JWT-shaped token (not sk-/rk_)
// paired with a ChatGPT-Account-ID header; the pair resolves to an OAuth
// subscription credential the Codex backend serves for free.
const (
	codexSubToken     = "eyJhbGciOi.codex.jwt"
	codexSubAccountID = "acct-codex-123"
)

// codexSubRequest builds an OpenAI chat-completions request carrying a Codex
// subscription in the inbound Authorization header.
func codexSubRequest(t *testing.T, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+codexSubToken)
	req.Header.Set("ChatGPT-Account-ID", codexSubAccountID)
	return httptest.NewRecorder(), req
}

// TestSubscriptionOnly_OpenAI_ServesOnCodexSub: in subscription-only mode, a
// Codex-covered turn routed to OpenAI must serve on the caller's own ChatGPT
// subscription (OAuth credential => $0 debit), dispatch exactly once (no paid
// failover), and surface the depleted-credits warning.
func TestSubscriptionOnly_OpenAI_ServesOnCodexSub(t *testing.T) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "test"}}
	// Direct OpenAI serves this turn on /v1/responses, so the upstream speaks
	// Responses SSE and the router translates it back to chat for the client.
	p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"hi\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderOpenAI: p}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)

	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":true}`
	rec, req := codexSubRequest(t, body)

	ctx := billing.WithSubscriptionOnly(context.Background())
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req))

	require.Len(t, p.proxyBodies, 1, "the turn must serve on the subscription exactly once (no paid failover)")
	require.NotNil(t, p.proxyCreds[0], "the dispatch must carry the caller's subscription credential")
	assert.True(t, p.proxyCreds[0].OAuth, "the turn must be served on the caller's own Codex subscription so billing debits $0")
	assert.Contains(t, rec.Body.String(), "credits are depleted", "the customer must see the depleted-credits warning")
	assert.Contains(t, rec.Body.String(), "ChatGPT (Codex)", "the warning must name the Codex subscription")
	assert.Contains(t, rec.Body.String(), "weave-router", "the warning must surface the top-up CTA")
}

// TestSubscriptionOnly_OpenAI_PaidRoute_Refuses402: a Codex-covered request
// that routing resolves to a paid provider (not served on the subscription)
// must be refused with the credits-exhausted sentinel and never dispatched —
// the bug the Codex path previously had, where such a turn debited past the
// floor with no bound.
func TestSubscriptionOnly_OpenAI_PaidRoute_Refuses402(t *testing.T) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenRouter, Model: "deepseek/deepseek-chat", Reason: "test"}}
	p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion"}`)
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderOpenRouter: p}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)

	// MainLoop-shaped (tools + large max_tokens) so the turn isn't classified as
	// a hard-pinned classifier turn; that would bypass the scorer and defeat the
	// paid-route scenario under test.
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Refactor the auth middleware and add tests."}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"edit_file","parameters":{"type":"object"}}}]}`
	rec, req := codexSubRequest(t, body)

	ctx := billing.WithSubscriptionOnly(context.Background())
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req)
	require.Error(t, err)
	require.Positive(t, fr.routeCalls, "the scorer must be consulted so the decision is the paid route under test")
	assert.True(t, errors.Is(err, proxy.ErrCreditsExhaustedSubscriptionUnavailable),
		"a Codex-covered turn that routes to a paid model must be refused, not dispatched")
	assert.Empty(t, p.proxyBodies, "no paid dispatch may occur below the floor in subscription-only mode")
}

func TestSubscriptionOnly_OpenAI_InfrastructureModelRefusesWithoutDispatch(t *testing.T) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.4-nano", Reason: "test"}}
	p := &fakeProvider{}
	svc := proxy.NewService(fr, map[string]providers.Client{
		providers.ProviderOpenAI: p,
	}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})

	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Refactor the auth middleware and add tests."}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"edit_file","parameters":{"type":"object"}}}]}`
	rec, req := codexSubRequest(t, body)

	err := svc.ProxyOpenAIChatCompletion(billing.WithSubscriptionOnly(context.Background()), []byte(body), rec, req)
	require.Error(t, err)
	assert.ErrorIs(t, err, proxy.ErrCreditsExhaustedSubscriptionUnavailable)
	require.NotNil(t, fr.capturedReq)
	assert.Contains(t, fr.capturedReq.ExcludedModels, "gpt-5.4-nano",
		"subscription-only candidate selection must exclude OpenAI models outside the native Codex family")
	assert.Empty(t, p.proxyBodies, "the infrastructure model must never dispatch against depleted credits")
}

// TestSubscriptionOnly_OpenAI_SubFailure_NoPaidFailover: when the caller's own
// subscription attempt fails (e.g. a 429 weekly-limit), paid failover is
// disabled — the turn must dispatch exactly once (its own sub) and surface the
// raw upstream error rather than reroute onto a paid model. The controlled 402
// is reserved for turns that can't run on the sub at all (see PaidRoute test);
// mislabeling a served-sub 429 as "credits exhausted" would be inaccurate.
func TestSubscriptionOnly_OpenAI_SubFailure_NoPaidFailover(t *testing.T) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "test"}}
	p := &fakeProvider{proxyErr: &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderOpenAI: p}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)

	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`
	rec, req := codexSubRequest(t, body)

	ctx := billing.WithSubscriptionOnly(context.Background())
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req)
	require.Error(t, err)
	var statusErr *providers.UpstreamStatusError
	require.True(t, errors.As(err, &statusErr), "the caller's own subscription error must surface raw, not be rewritten")
	assert.Equal(t, http.StatusTooManyRequests, statusErr.Status)
	assert.False(t, errors.Is(err, proxy.ErrCreditsExhaustedSubscriptionUnavailable),
		"a served-sub runtime failure is not a credits-exhausted refusal")
	require.NotNil(t, p.proxyCreds[0], "the dispatch must carry the caller's subscription credential")
	assert.True(t, p.proxyCreds[0].OAuth, "only the caller's own subscription may be attempted")
	assert.Len(t, p.proxyBodies, 1, "only the subscription attempt may dispatch; no paid failover")
}
