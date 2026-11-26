package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

// goldenCodexQuotaUnverified is the preflight failure the OpenAI client returns
// when the ChatGPT usage endpoint times out: it says nothing about the plan.
func goldenCodexQuotaUnverified() goldenStep {
	body := `{"error":{"type":"api_error","message":"Included Codex quota could not be verified."}}`
	return goldenStep{err: &providers.UpstreamErrorResponse{
		Status:    http.StatusServiceUnavailable,
		Headers:   http.Header{"Content-Type": {"application/json"}},
		Body:      []byte(body),
		BodyBytes: int64(len(body)),
		Cause:     &providers.QuotaVerificationUnavailableError{Failure: providers.QuotaVerificationFailureTimeout},
	}}
}

func goldenCodexLoginSiblingModelsCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel, goldenCodexSiblingModel}
	ctx = goldenCodexCtx(ctx)
	ctx = requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{ClientApp: requestcontext.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	return credentials.WithManagedSubscriptionUsage(ctx)
}

// goldenCodexQuotaUnverifiedScenarios pin a Codex turn whose quota preflight
// timed out once on the caller's own login: the plan is not known to be spent,
// so the turn stays on the picked model instead of hopping to a sibling.
func goldenCodexQuotaUnverifiedScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressResponses + "_stream_codex_quota_unverified_keeps_picked_model", ingress: ingressResponses,
		decisions: []router.Decision{
			decision(providers.ProviderOpenAI, goldenCodexModel),
			decision(providers.ProviderOpenAI, goldenCodexSiblingModel),
		},
		upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
			goldenCodexQuotaUnverified(),
			goldenResponsesOK("served on the picked model"),
		}},
		requests: []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0e1"), decorateContext: goldenCodexLoginSiblingModelsCtx}},
	}, {
		name: ingressResponses + "_stream_codex_quota_unverified_repeatedly_marker_names_check", ingress: ingressResponses,
		decisions: []router.Decision{
			decision(providers.ProviderOpenAI, goldenCodexModel),
			decision(providers.ProviderOpenAI, goldenCodexSiblingModel),
		},
		upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
			goldenCodexQuotaUnverified(), goldenCodexQuotaUnverified(), goldenCodexQuotaUnverified(),
			goldenResponsesOK("served after the check recovered"),
		}},
		requests: []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0e2"), decorateContext: goldenCodexLoginSiblingModelsCtx}},
	}}
}

// TestCodexQuotaUnverifiedKeepsPickedModel states the invariant: a quota check
// that could not complete is neither capacity nor model evidence, so the turn
// is retried on the picked model and the marker never claims the plan had no
// subscription capacity.
func TestCodexQuotaUnverifiedKeepsPickedModel(t *testing.T) {
	t.Parallel()
	sc := goldenCodexQuotaUnverifiedScenarios()[0]
	served := newGoldenHarness(t, sc).serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	for _, attempt := range served.Attempts {
		require.Equal(t, goldenCodexModel, attempt.Model, "an unverified quota check moved the turn off the picked model")
		require.Equal(t, "codex_subscription", attempt.Credential)
	}
	require.Equal(t, "ok", served.Attempts[len(served.Attempts)-1].Outcome)
	raw, err := json.Marshal(served.Response)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "had no subscription capacity")
	require.Zero(t, goldenCreditDebits(t, served), "a turn served on the caller's plan draws no Weave credits")
}

// TestCodexQuotaUnverifiedMarkerNamesCheck states that when the quota check
// keeps failing and the turn does move, the marker blames the check rather
// than claiming the plan had no subscription capacity.
func TestCodexQuotaUnverifiedMarkerNamesCheck(t *testing.T) {
	t.Parallel()
	sc := goldenCodexQuotaUnverifiedScenarios()[1]
	served := newGoldenHarness(t, sc).serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	raw, err := json.Marshal(served.Response)
	require.NoError(t, err)
	body := string(raw)
	require.Contains(t, body, "switched after "+goldenCodexModel+" could not verify its subscription quota")
	require.NotContains(t, body, "had no subscription capacity")
}
