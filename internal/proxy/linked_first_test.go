package proxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexResponsesStream is the minimal upstream Responses stream a routed
// OpenAI turn needs to complete.
func codexResponsesStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"hi\"}\n\n")
	_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
}

// spentCodexService wires a Codex-covered OpenAI route whose usage observer has
// already seen the caller's ChatGPT plan bind its 5h window, with a deployment
// OpenAI key available to serve the turn instead.
func spentCodexService(p *fakeProvider) *proxy.Service {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol", Reason: "test"}}
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(codexSubToken)), usage.Snapshot{
		Primary: usage.Window{UsedPercent: 1.0, WindowMinutes: 300},
	})
	return proxy.NewService(fr, map[string]providers.Client{providers.ProviderOpenAI: p}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil).
		WithSubscriptionAwareRouting(obs, 0.05, 2.0).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}})
}

// TestLinkedFirst_OpenAI_SpentCodexPlan_ContinuesOnWeaveKey: a linked-first
// turn whose ChatGPT plan window has bound must roll over to the deployment
// key and the organization's credits — the linked plan is a funding
// preference, and the balance gate already admitted the turn. Refusing it as
// "credits exhausted" was the incident this guards against: a funded org
// locked out of Codex for the rest of the plan window.
func TestLinkedFirst_OpenAI_SpentCodexPlan_ContinuesOnWeaveKey(t *testing.T) {
	p := &fakeProvider{proxyResponse: codexResponsesStream}
	svc := spentCodexService(p)

	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":true}`
	rec, req := codexSubRequest(t, body)

	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyLinkedFirst)
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req))

	require.Len(t, p.proxyBodies, 1, "the turn must dispatch once on the fallback key")
	if p.proxyCreds[0] != nil {
		assert.False(t, p.proxyCreds[0].OAuth, "the spent ChatGPT plan must not be dispatched on")
	}
	assert.NotContains(t, rec.Body.String(), "credits are depleted",
		"a credit-funded turn must not claim the organization's credits are gone")
}

// TestSubscriptionOnly_OpenAI_SpentCodexPlan_DepletedStillRefuses402: the
// rollover is scoped to linked-first. With credits genuinely depleted there is
// nothing to fall through to, so a spent plan is still refused, never billed.
func TestSubscriptionOnly_OpenAI_SpentCodexPlan_DepletedStillRefuses402(t *testing.T) {
	p := &fakeProvider{proxyResponse: codexResponsesStream}
	svc := spentCodexService(p)

	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":true}`
	rec, req := codexSubRequest(t, body)

	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
	err := svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req)
	require.Error(t, err)
	assert.True(t, errors.Is(err, proxy.ErrCreditsExhaustedSubscriptionUnavailable))
	assert.Empty(t, p.proxyBodies, "no paid dispatch may occur against depleted credits")
}

// TestLinkedFirst_OpenAI_PaidRoute_ContinuesOnCredits: a linked-first turn that
// routing resolves to a model the linked plan can't pay for continues on
// organization credits instead of the credits-exhausted 402 a depleted turn
// gets (compare TestSubscriptionOnly_OpenAI_PaidRoute_Refuses402).
func TestLinkedFirst_OpenAI_PaidRoute_ContinuesOnCredits(t *testing.T) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenRouter, Model: "deepseek/deepseek-chat", Reason: "test"}}
	p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}}
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderOpenRouter: p}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil)

	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Refactor the auth middleware and add tests."}],"max_tokens":4096,"tools":[{"type":"function","function":{"name":"edit_file","parameters":{"type":"object"}}}]}`
	rec, req := codexSubRequest(t, body)

	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyLinkedFirst)
	require.NoError(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, req))
	require.Positive(t, fr.routeCalls, "the scorer must be consulted so the decision is the paid route under test")
	require.Len(t, p.proxyBodies, 1, "the paid route must dispatch on organization credits")
}

// TestLinkedFirst_Anthropic_SpentClaudePlan_ContinuesOnWeaveKey is the Claude
// counterpart: an observed-exhausted Claude plan on a linked-first turn rolls
// over to the deployment key instead of the 402 the depleted path keeps
// (compare TestSubscriptionOnly_ExhaustedSubscription_Refuses402).
func TestLinkedFirst_Anthropic_SpentClaudePlan_ContinuesOnWeaveKey(t *testing.T) {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: bypassScorerPickMdl}}
	p := &fakeProvider{proxyResponse: bypassStreamResponse}
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(bypassSubToken)), usage.Snapshot{
		Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080},
	})
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: p}, nil, false, nil, nil, false, providers.ProviderAnthropic, bypassScorerPickMdl, nil).
		WithSubscriptionAwareRouting(obs, 0.05, 2.0).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})

	rec, req, body := bypassRequest(t)
	ctx := billing.WithSubscriptionOnly(bypassCtx(0.80), billing.SubscriptionOnlyLinkedFirst)
	require.NoError(t, svc.ProxyMessages(ctx, body, rec, req))

	require.Len(t, p.proxyBodies, 1, "the turn must dispatch once on the fallback key")
	if p.proxyCreds[0] != nil {
		assert.False(t, p.proxyCreds[0].OAuth, "the spent Claude plan must not be dispatched on")
	}
	assert.NotContains(t, rec.Body.String(), "credits are depleted",
		"a credit-funded turn must not claim the organization's credits are gone")
}
