package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/api/admin"
	"workweave/router/internal/router/policy"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRosterSource struct {
	snapshot policy.RosterSnapshot
}

func (f fakeRosterSource) ClusterRoster(context.Context) (policy.RosterSnapshot, error) {
	return f.snapshot, nil
}

func TestHMMRosterHandler_PreservesRosterArmsAndCatalogModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.GET("/v1/router/hmm-roster", admin.HMMRosterHandler(fakeRosterSource{
		snapshot: policy.RosterSnapshot{
			RosterSHA256: "sha-1",
			Clusters: map[string][]string{
				"high": {
					"anthropic/claude-opus-5:xhigh",
					"x-ai/grok-4.6",
				},
			},
		},
	}))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/router/hmm-roster", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Clusters []struct {
			Arms   []string `json:"arms"`
			Models []string `json:"models"`
		} `json:"clusters"`
		RosterSHA256 string `json:"roster_sha256"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Clusters, 1)
	assert.Equal(t, "sha-1", body.RosterSHA256)
	assert.Equal(t, []string{
		"anthropic/claude-opus-5:xhigh",
		"x-ai/grok-4.6",
	}, body.Clusters[0].Arms)
	assert.Equal(t, []string{"claude-opus-5", "grok-4.6"}, body.Clusters[0].Models)
}
