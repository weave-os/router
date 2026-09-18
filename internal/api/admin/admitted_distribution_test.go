package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/hmm/rosterdata"
)

func profileDistributionSnapshot(arm string) *policyregistry.Snapshot {
	return &policyregistry.Snapshot{Candidate: policyregistry.Candidate{Policy: &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionPolicyV1,
		Ranking:       rosterdata.Ranking{Alpha: map[string]float64{"low": 0.4}, AlphaMin: map[string]float64{"low": 0.05}, AlphaMax: map[string]float64{"low": 0.8}, QualityBiasNeutral: 0.7},
		Clusters: map[string]rosterdata.Cluster{"low": {
			Arms: []string{arm}, ArmScores: map[string]float64{arm: 20}, ArmIndices: map[string]rosterdata.ArmIndices{arm: {WII: 50, WPI: 0}},
		}},
	}}}
}

func TestAdmittedRoutingDistributionUsesEachRequestsPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v1/router/routing-distribution", admin.AdmittedRoutingDistributionHandler(policyregistry.AdmittedRosterSource{}))
	for _, profile := range []struct{ arm, model string }{
		{"openai/gpt-5.6-luna", "gpt-5.6-luna"},
		{"x-ai/grok-4.6", "grok-4.6"},
		{"openai/gpt-5.6-luna", "gpt-5.6-luna"},
	} {
		for _, strategy := range []router.Strategy{"", router.StrategyHMM, router.StrategyHMMEmbedding} {
			request := httptest.NewRequest(http.MethodGet, "/v1/router/routing-distribution?grid=2&strategy="+string(strategy), nil)
			request = request.WithContext(policyregistry.WithServingSnapshot(request.Context(), profileDistributionSnapshot(profile.arm)))
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var response struct {
				Points []cluster.DistributionPoint `json:"points"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Len(t, response.Points, 2)
			for _, point := range response.Points {
				require.Len(t, point.Models, 1)
				assert.Equal(t, profile.model, point.Models[0].Model)
			}
		}
	}
}

func TestAdmittedRoutingDistributionFailsClosedWithoutSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v1/router/routing-distribution", admin.AdmittedRoutingDistributionHandler(policyregistry.AdmittedRosterSource{}))
	for _, strategy := range []router.Strategy{"", router.StrategyHMM, router.StrategyHMMEmbedding} {
		request := httptest.NewRequest(http.MethodGet, "/v1/router/routing-distribution?strategy="+string(strategy), nil)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	}
}

func TestAdmittedRoutingDistributionRejectsNonHMMAndAppliesExclusions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v1/router/routing-distribution", admin.AdmittedRoutingDistributionHandler(policyregistry.AdmittedRosterSource{}))
	for _, query := range []string{"strategy=" + string(router.StrategyCluster), "strategy=" + string(router.StrategyRL), "strategy=" + string(router.StrategyHMMBeta), "strategy=unknown", "excluded_models=gpt-5.6-luna", "grid=102"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/router/routing-distribution?"+query, nil)
		request = request.WithContext(policyregistry.WithServingSnapshot(request.Context(), profileDistributionSnapshot("openai/gpt-5.6-luna")))
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, query)
	}
}
