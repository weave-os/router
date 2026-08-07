package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fencedInstallationCtx is what the auth middleware builds for an installation
// carrying an egress fence.
func fencedInstallationCtx(installationID string, allowed ...string) context.Context {
	return context.WithValue(authedCtx(installationID), proxy.InstallationAllowedProvidersContextKey{}, allowed)
}

// openAIOKResponse is a minimal well-formed Chat Completions body, so a
// dispatch that does reach OpenAI completes instead of dying in translation.
func openAIOKResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`)
}

func fenceSvc(fr *fakeRouter, store *fakePinStore, clients map[string]providers.Client) *proxy.Service {
	keyed := make(map[string]struct{}, len(clients))
	for p := range clients {
		keyed[p] = struct{}{}
	}
	return proxy.NewService(
		fr, clients, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	).WithDeploymentKeyedProviders(keyed)
}

// A sticky pin is the strongest bypass in the turn loop — it skips the scorer
// entirely — so a pin written before the fence narrowed must not keep serving
// a provider the installation may no longer reach.
func TestProxyMessages_SessionPinOutsideFenceIsDropped(t *testing.T) {
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:      providers.ProviderOpenAI,
		Model:         "gpt-5.5",
		Reason:        "cluster:v0.57",
		PinnedUntil:   time.Now().Add(30 * time.Minute),
		FirstPinnedAt: time.Now().Add(-5 * time.Minute),
	}
	openai := &fakeProvider{proxyResponse: openAIOKResponse}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-7", Reason: "cluster:v0.57"}}
	svc := fenceSvc(fr, store, map[string]providers.Client{
		providers.ProviderAnthropic: &fakeProvider{},
		providers.ProviderOpenAI:    openai,
	})

	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(
		fencedInstallationCtx(uuid.New().String(), providers.ProviderAnthropic),
		[]byte(pinTestBody), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")),
	)

	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-7", rec.Header().Get(proxy.HeaderRouterModel),
		"the fenced-off pin must give way to a fresh in-fence decision")
	assert.Empty(t, openai.proxyBodies, "the fenced-off provider must not be dispatched to")
	require.NotNil(t, fr.capturedReq)
	assert.NotContains(t, fr.capturedReq.EnabledProviders, providers.ProviderOpenAI,
		"the scorer must not be offered a provider outside the fence")
}

// /force-model is an explicit user override that outranks the planner and the
// tier ceiling. It must not outrank the fence.
func TestProxyMessages_ForcedPinOutsideFenceIsDropped(t *testing.T) {
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:      providers.ProviderOpenAI,
		Model:         "gpt-5.5",
		Reason:        translate.ReasonUserForceModel,
		PinnedUntil:   time.Now().Add(365 * 24 * time.Hour),
		FirstPinnedAt: time.Now().Add(-time.Minute),
	}
	openai := &fakeProvider{proxyResponse: openAIOKResponse}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-7", Reason: "cluster:v0.57"}}
	svc := fenceSvc(fr, store, map[string]providers.Client{
		providers.ProviderAnthropic: &fakeProvider{},
		providers.ProviderOpenAI:    openai,
	})

	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(
		fencedInstallationCtx(uuid.New().String(), providers.ProviderAnthropic),
		[]byte(pinTestBody), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")),
	)

	require.NoError(t, err)
	assert.Equal(t, providers.ProviderAnthropic, rec.Header().Get(proxy.HeaderRouterProvider))
	assert.Empty(t, openai.proxyBodies, "a user-forced pin must not carry a request past the fence")
}

// The fence is a boundary, not a preference: with nothing left to serve, the
// request must fail rather than quietly reaching for a provider outside it.
func TestProxyMessages_FenceWithNoServableProviderFailsClosed(t *testing.T) {
	anthropic := &fakeProvider{}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-7", Reason: "cluster:v0.57"}}
	svc := fenceSvc(fr, newFakePinStore(), map[string]providers.Client{
		providers.ProviderAnthropic: anthropic,
	})

	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(
		// Fenced to a provider this deployment doesn't even run.
		fencedInstallationCtx(uuid.New().String(), providers.ProviderBedrock),
		[]byte(pinTestBody), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")),
	)

	require.Error(t, err)
	assert.Empty(t, anthropic.proxyBodies, "no upstream may be called once the fence empties the pool")
	cls, ok := proxy.ClassifyDispatchError(err)
	require.True(t, ok, "the failure must classify, not fall through as an unlabeled 500")
	assert.True(t, cls.Kind.IsClientError(),
		"the deployment is healthy — this is the caller's configuration, not our outage")
}

// Unfenced installations are the overwhelming majority; the fence must be
// inert for them.
func TestProxyMessages_UnfencedInstallationIsUnaffected(t *testing.T) {
	openai := &fakeProvider{proxyResponse: openAIOKResponse}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.5", Reason: "cluster:v0.57"}}
	svc := fenceSvc(fr, newFakePinStore(), map[string]providers.Client{
		providers.ProviderAnthropic: &fakeProvider{},
		providers.ProviderOpenAI:    openai,
	})

	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(
		authedCtx(uuid.New().String()),
		[]byte(pinTestBody), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")),
	)

	require.NoError(t, err)
	assert.Len(t, openai.proxyBodies, 1, "an installation with no fence keeps reaching every deployed provider")
}

// The deployment-wide fence has to hold for installations that carry no fence
// of their own — that's the whole point of a deploy-level boundary.
func TestProxyMessages_DeploymentOverrideFencesUnfencedInstallations(t *testing.T) {
	openai := &fakeProvider{proxyResponse: openAIOKResponse}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.5", Reason: "cluster:v0.57"}}
	svc := fenceSvc(fr, newFakePinStore(), map[string]providers.Client{
		providers.ProviderAnthropic: &fakeProvider{},
		providers.ProviderOpenAI:    openai,
	}).WithAllowedProvidersOverride([]string{providers.ProviderAnthropic})

	rec := httptest.NewRecorder()
	err := svc.ProxyMessages(
		authedCtx(uuid.New().String()),
		[]byte(pinTestBody), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("")),
	)

	require.Error(t, err)
	require.ErrorIs(t, err, proxy.ErrProviderNotAllowed)
	assert.Empty(t, openai.proxyBodies)
}
