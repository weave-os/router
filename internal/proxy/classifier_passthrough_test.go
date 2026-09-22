package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	classifierPassthroughReason = "classifier_subscription_passthrough"
	classifierRequestedMdl      = "claude-haiku-4-5"
	classifierScorerPickMdl     = "claude-opus-4-7"
)

// classifierPassthroughFixture wires a scorer that would substitute
// classifierScorerPickMdl, a pin store that records every read/write, and a
// fake Anthropic upstream that answers 200. No usage-bypass config is placed on
// ctx anywhere in this file: classifier passthrough must not depend on it.
func classifierPassthroughFixture(t *testing.T, obs *usage.Observer) (*proxy.Service, *fakeRouter, *fakeProvider, *fakePinStore) {
	t.Helper()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: classifierScorerPickMdl, Reason: "cluster:v0.2"}}
	p := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + classifierRequestedMdl + `","content":[{"type":"text","text":"<severity>0</severity>"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}}
	store := newFakePinStore()
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: p}, nil, false, nil, store, false, providers.ProviderAnthropic, classifierRequestedMdl, nil)
	if obs != nil {
		svc = svc.WithSubscriptionAwareRouting(obs, 0.05, 2.0)
	}
	return svc, fr, p, store
}

func classifierSubscriptionCtx() context.Context {
	return context.WithValue(authedCtx(uuid.New().String()), proxy.AnthropicSubscriptionContextKey{}, bypassSubToken)
}

func classifierRequest() (*httptest.ResponseRecorder, *http.Request) {
	return httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
}

func exhaustedObserver() *usage.Observer {
	obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
	obs.Record(obs.Key([]byte(bypassSubToken)), usage.Snapshot{
		Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080},
	})
	return obs
}

func assertNoPinWrite(t *testing.T, store *fakePinStore) {
	t.Helper()
	select {
	case <-store.upsertCh:
		t.Fatal("a classifier must never write a session pin")
	case <-time.After(100 * time.Millisecond):
	}
}

// A classifier arriving with the caller's Claude subscription is Anthropic's
// own Auto-mode security check, billed to the plan. It passes straight through
// to the requested model: no scorer, no opt-in, no session pin.
func TestClassifier_SubscriptionPassthrough_SkipsScorer(t *testing.T) {
	svc, fr, p, store := classifierPassthroughFixture(t, nil)
	rec, req := classifierRequest()

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(classifierBody), rec, req))

	assert.Equal(t, 0, fr.routeCalls, "a subscription classifier must not be scored")
	require.Len(t, p.proxyBodies, 1)
	assert.Contains(t, string(p.proxyBodies[0]), `"model":"`+classifierRequestedMdl+`"`, "passthrough must preserve the requested model")
	assert.Equal(t, classifierPassthroughReason, rec.Header().Get(proxy.HeaderRouterDecision))
	assert.Equal(t, classifierRequestedMdl, rec.Header().Get(proxy.HeaderRouterModel))
	require.NotNil(t, p.proxyCreds[0])
	assert.True(t, p.proxyCreds[0].OAuth, "the dispatch must carry the caller's own subscription credential")
	assert.Equal(t, 1, store.getCalls, "only the force-model lookup may touch the pin store")
	assertNoPinWrite(t, store)
}

// The passthrough is keyed on the Claude subscription, not on the
// installation's usage-bypass opt-in: a classifier with no subscription
// credential still takes the scorer's verdict (API-key callers are billed for
// the classifier either way).
func TestClassifier_NoSubscription_IsScored(t *testing.T) {
	svc, fr, _, store := classifierPassthroughFixture(t, nil)
	rec, req := classifierRequest()

	require.NoError(t, svc.ProxyMessages(authedCtx(uuid.New().String()), []byte(classifierBody), rec, req))

	assert.Equal(t, 1, fr.routeCalls)
	assert.Equal(t, classifierScorerPickMdl, rec.Header().Get(proxy.HeaderRouterModel))
	assertNoPinWrite(t, store)
}

// A main-loop turn with the same subscription and no usage-bypass opt-in is
// unchanged: the classifier lane must not widen into a general passthrough.
func TestClassifier_MainLoopWithSubscription_StillScored(t *testing.T) {
	svc, fr, _, _ := classifierPassthroughFixture(t, nil)
	rec, req := classifierRequest()
	mainLoop := `{"model":"` + classifierRequestedMdl + `","max_tokens":4096,"messages":[{"role":"user","content":"hello"}]}`

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(mainLoop), rec, req))

	assert.Equal(t, 1, fr.routeCalls, "without the usage-bypass opt-in a main-loop turn is scored")
	assert.NotEqual(t, classifierPassthroughReason, rec.Header().Get(proxy.HeaderRouterDecision))
}

// An explicit /force-model outranks the passthrough, as it outranks every
// automatic fast path.
func TestClassifier_ForceModel_OutranksPassthrough(t *testing.T) {
	svc, fr, p, store := classifierPassthroughFixture(t, nil)
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider: providers.ProviderAnthropic, Model: classifierScorerPickMdl,
		Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(24 * time.Hour),
	}
	rec, req := classifierRequest()

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(classifierBody), rec, req))

	assert.Equal(t, 0, fr.routeCalls)
	assert.Equal(t, translate.ReasonUserForceModel, rec.Header().Get(proxy.HeaderRouterDecision))
	assert.Equal(t, classifierScorerPickMdl, rec.Header().Get(proxy.HeaderRouterModel))
	require.Len(t, p.proxyBodies, 1)
	assert.Contains(t, string(p.proxyBodies[0]), `"model":"`+classifierScorerPickMdl+`"`)
}

// An observed-exhausted subscription would 429; the passthrough disengages and
// the scored turn serves on the deployment key when one exists.
func TestClassifier_ExhaustedSubscription_ServesOnDeploymentKey(t *testing.T) {
	svc, fr, p, store := classifierPassthroughFixture(t, exhaustedObserver())
	svc = svc.WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	rec, req := classifierRequest()

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(classifierBody), rec, req))

	assert.Equal(t, 1, fr.routeCalls, "an exhausted subscription must hand the classifier to the scorer")
	assert.NotEqual(t, classifierPassthroughReason, rec.Header().Get(proxy.HeaderRouterDecision))
	require.Len(t, p.proxyCreds, 1)
	if creds := p.proxyCreds[0]; creds != nil {
		assert.False(t, creds.OAuth, "the spent subscription must not be forwarded when a fallback key exists")
	}
	assertNoPinWrite(t, store)
}

// With no deployment / BYOK key to fall through to, dropping the subscription
// would leave the turn with no credential; the scored turn keeps it.
func TestClassifier_ExhaustedSubscription_NoFallbackKeepsSubscription(t *testing.T) {
	svc, fr, p, _ := classifierPassthroughFixture(t, exhaustedObserver())
	rec, req := classifierRequest()

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(classifierBody), rec, req))

	assert.Equal(t, 1, fr.routeCalls)
	require.Len(t, p.proxyCreds, 1)
	require.NotNil(t, p.proxyCreds[0])
	assert.True(t, p.proxyCreds[0].OAuth)
}

// Subscription-only mode with an exhausted subscription: the passthrough
// disengages and the scored turn is refused rather than billed.
func TestClassifier_SubscriptionOnly_Exhausted_Refuses402(t *testing.T) {
	svc, _, p, _ := classifierPassthroughFixture(t, exhaustedObserver())
	rec, req := classifierRequest()

	err := svc.ProxyMessages(billing.WithSubscriptionOnly(classifierSubscriptionCtx(), billing.SubscriptionOnlyCreditsDepleted), []byte(classifierBody), rec, req)

	require.Error(t, err)
	assert.True(t, errors.Is(err, proxy.ErrCreditsExhaustedSubscriptionUnavailable))
	assert.Empty(t, p.proxyBodies, "no paid dispatch may occur when the subscription cannot serve the classifier")
}

// A passthrough that hits a retryable upstream error falls back to the routed
// path exactly like usage bypass, and the reroute still anchors no pin.
func TestClassifier_PassthroughRetryable_FallsBackToRoutedDispatch_NoPin(t *testing.T) {
	limit := &providers.UpstreamErrorResponse{
		Status: http.StatusTooManyRequests,
		Headers: http.Header{
			"anthropic-ratelimit-unified-weekly-limit":     []string{"100000"},
			"anthropic-ratelimit-unified-weekly-reset":     []string{"2025-12-31T00:00:00Z"},
			"anthropic-ratelimit-unified-weekly-remaining": []string{"0"},
		},
		Body: []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"weekly limit exceeded"}}`),
	}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: classifierScorerPickMdl, Reason: "cluster:v0.2"}}
	inner := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"` + classifierScorerPickMdl + `","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}}
	upstream := &swapErrProvider{first: limit, second: nil, inner: inner}
	store := newFakePinStore()
	svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: upstream}, nil, false, nil, store, false, providers.ProviderAnthropic, classifierRequestedMdl, nil)
	rec, req := classifierRequest()

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(classifierBody), rec, req))

	assert.Equal(t, 2, upstream.calls, "passthrough attempt, then the routed dispatch")
	assert.Equal(t, 1, fr.routeCalls, "the scorer runs once, on the reroute")
	assert.NotEqual(t, http.StatusTooManyRequests, rec.Code, "the passthrough 429 must not reach the client")
	assert.Equal(t, classifierScorerPickMdl, rec.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, "cluster:v0.2", rec.Header().Get(proxy.HeaderRouterDecision))
	assert.Equal(t, 1, store.getCalls, "the reroute must not load the conversation's pin for a classifier")
	assertNoPinWrite(t, store)
}
