package policyclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestClientPreservesNullableMarginAcrossSchemas(t *testing.T) {
	for _, schema := range []string{"", policy.SchemaVersionV1, policy.SchemaVersionV2, policy.SchemaVersionV3, policy.SchemaVersionV4} {
		for _, tc := range []struct {
			name   string
			margin *float64
		}{
			{name: "present", margin: floatPtr(0.22)},
			{name: "zero", margin: floatPtr(0)},
			{name: "absent"},
		} {
			t.Run(schema+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					var payload map[string]any
					if schema == policy.SchemaVersionV4 {
						encoded, err := json.Marshal(validClassifierResponseV4())
						require.NoError(t, err)
						require.NoError(t, json.Unmarshal(encoded, &payload))
					} else {
						payload = map[string]any{"schema_version": schema, "model": "test-model"}
						if schema == policy.SchemaVersionV3 {
							delete(payload, "model")
							payload["ranked_fallback"] = []policy.PreviewGroup{{Group: "test-group"}}
						}
					}
					delete(payload, "classifier_margin")
					if tc.margin != nil {
						payload["classifier_margin"] = *tc.margin
					}
					require.NoError(t, json.NewEncoder(w).Encode(payload))
				}))
				defer server.Close()
				classification, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{SchemaVersion: schema, Strategy: router.StrategyHMM})
				require.NoError(t, err)
				assert.Equal(t, tc.margin, classification.Margin)
			})
		}
	}
}

func TestClientLegacyMarginTakesPrecedenceIncludingZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(routeResponse{Model: "test-model", Margin: floatPtr(0), ClassifierMargin: floatPtr(0.4)}))
	}))
	defer server.Close()
	classification, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{})
	require.NoError(t, err)
	assert.Equal(t, floatPtr(0), classification.Margin)
}
