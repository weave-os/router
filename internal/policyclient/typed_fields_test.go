package policyclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/policy"
)

func TestClientDecideParsesTypedContractFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(routeResponse{
			SchemaVersion:      policy.SchemaVersionV1,
			SelectedRosterID:   "anthropic/claude-opus-4-8",
			PredictedLabel:     "high",
			ClassProbabilities: map[string]float64{"high": 0.7, "balanced": 0.3},
		})
	}))
	defer server.Close()

	result, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		Candidates: []policy.Candidate{{RosterID: "anthropic/claude-opus-4-8", CatalogID: "claude-opus-4-8", Provider: providers.ProviderAnthropic}},
	})

	require.NoError(t, err)
	assert.Equal(t, "high", result.PredictedLabel)
	assert.Equal(t, map[string]float64{"high": 0.7, "balanced": 0.3}, result.ClassProbabilities)
}

// TestClientDecideLeavesTypedFieldsUnsetOnV1OnlyResponse verifies that a v1
// response without typed fields leaves PredictedLabel/ClassProbabilities nil/empty.
func TestClientDecideLeavesTypedFieldsUnsetOnV1OnlyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"schema_version": "policy_router_v1",
			"route_id": "route-v1",
			"selected_roster_id": "anthropic/claude-opus-4-8",
			"selected_provider": "anthropic",
			"score": 0.42,
			"reason": "classifier group 'high'",
			"policy_group": "high",
			"confidence": 0.42,
			"propensity": 1.0
		}`))
	}))
	defer server.Close()

	result, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		Candidates: []policy.Candidate{{RosterID: "anthropic/claude-opus-4-8", CatalogID: "claude-opus-4-8", Provider: providers.ProviderAnthropic}},
	})

	require.NoError(t, err)
	assert.Equal(t, "route-v1", result.RouteID)
	assert.Equal(t, "anthropic/claude-opus-4-8", result.Model)
	assert.Equal(t, "classifier group 'high'", result.Reason)
	assert.Equal(t, "high", result.PolicyGroup)
	assert.Equal(t, 0.42, result.Score)
	assert.Empty(t, result.PredictedLabel)
	assert.Nil(t, result.ClassProbabilities)
}
