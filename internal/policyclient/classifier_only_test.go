package policyclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/policy"
)

func classifierOnlyClient(t *testing.T, body string) (policy.Result, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	return New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		Candidates: []policy.Candidate{{RosterID: "anthropic/claude-opus-4-8", CatalogID: "claude-opus-4-8", Provider: providers.ProviderAnthropic}},
	})
}

func TestClientDecideAcceptsClassifierOnlyResponse(t *testing.T) {
	result, err := classifierOnlyClient(t, `{
		"schema_version": "policy_router_v3",
		"route_id": "route-v3",
		"selected_roster_id": null,
		"selected_provider": null,
		"model": null,
		"score": 0.42,
		"reason": "classifier group 'high'",
		"policy_group": "high",
		"predicted_label": "high",
		"class_probabilities": {"high": 0.7, "balanced": 0.3},
		"ranked_fallback": [
			{"group": "high", "probability": 0.7, "roster_arms": ["anthropic/claude-opus-4-8"], "eligible_arms": ["anthropic/claude-opus-4-8"]}
		]
	}`)

	require.NoError(t, err)
	assert.Empty(t, result.Model, "a v3 body carries no served arm; the caller selects one")
	assert.Equal(t, "high", result.PredictedLabel)
	require.Len(t, result.RankedFallback, 1)
	assert.Equal(t, []string{"anthropic/claude-opus-4-8"}, result.RankedFallback[0].EligibleArms)
}

func TestClientDecideRejectsSelectedArmOnClassifierOnlySchema(t *testing.T) {
	_, err := classifierOnlyClient(t, `{
		"schema_version": "policy_router_v3",
		"route_id": "route-v3",
		"selected_roster_id": "anthropic/claude-opus-4-8",
		"ranked_fallback": [
			{"group": "high", "probability": 0.7, "roster_arms": ["anthropic/claude-opus-4-8"], "eligible_arms": ["anthropic/claude-opus-4-8"]}
		]
	}`)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "selected arm")
}

func TestClientDecideRejectsClassifierOnlyResponseWithoutRankedFallback(t *testing.T) {
	_, err := classifierOnlyClient(t, `{
		"schema_version": "policy_router_v3",
		"route_id": "route-v3",
		"predicted_label": "high"
	}`)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ranked fallback",
		"without a ranking there is nothing to select from, so the turn must fail rather than serve a default")
}
