package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
)

func blindExperimentPassthroughContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active:              true,
		Arm:                 auth.BlindExperimentArmPassthrough,
		AssignmentSource:    auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1",
	})
}

func TestProxyOpenAIResponses_BlindPassthroughDoesNotRescueOrMutatePins(t *testing.T) {
	upstreams := &cyberRefusalUpstreams{openAIResponse: streamResponses(cyberRefusalSSE)}
	openAIURL, anthropicURL := upstreams.start(t)
	store := newFakePinStore()
	svc := cyberRefusalService(openAIURL, anthropicURL, "scorer-decision", store, newCaptureTelemetry())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	ctx := blindExperimentPassthroughContext(authedCtx(cyberRefusalInstallationID))
	ctx = context.WithValue(ctx, proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})

	_ = svc.ProxyOpenAIResponses(ctx, []byte(responsesTurnBody), recorder, request)

	openAIHits, anthropicHits := upstreams.counts()
	assert.Equal(t, 1, openAIHits)
	assert.Zero(t, anthropicHits, "passthrough must not substitute a fallback model after a refusal")
	assert.Contains(t, recorder.Body.String(), "cybersecurity risk", "the requested model's response must be returned unchanged")
	assert.Equal(t, "gpt-5.6-sol", recorder.Header().Get(proxy.HeaderRouterModel))

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, 1, store.getCalls, "passthrough may read only the explicit force-model control row")
	assert.Empty(t, store.upserts, "passthrough must leave automatic pins untouched")
	assert.Empty(t, store.usages, "passthrough must not write automatic pin history")
	assert.Zero(t, store.incrementCalls)
	assert.Zero(t, store.resetCalls)
	assert.Zero(t, store.overloadIncrementCalls)
	assert.Zero(t, store.overloadResetCalls)
	assert.Empty(t, store.disabledProviders)
}
