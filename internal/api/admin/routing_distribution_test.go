package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm/rosterdata"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDistributionRoster struct {
	roster     *rosterdata.Roster
	err        error
	strategies []router.Strategy
}

func (f *fakeDistributionRoster) DistributionRoster(ctx context.Context) (*rosterdata.Roster, error) {
	f.strategies = append(f.strategies, router.StrategyFromContext(ctx))
	return f.roster, f.err
}

func twoArmDistributionRoster() *rosterdata.Roster {
	return &rosterdata.Roster{
		SchemaVersion: rosterdata.SchemaVersionV7,
		Ranking: rosterdata.Ranking{
			Alpha: map[string]float64{"low": 0.4}, AlphaMin: map[string]float64{"low": 0.05},
			AlphaMax: map[string]float64{"low": 0.8}, QualityBiasNeutral: 0.7,
		},
		Clusters: map[string]rosterdata.Cluster{
			"low": {
				Arms:      []string{"openai/gpt-5.6-luna", "x-ai/grok-4.6"},
				ArmScores: map[string]float64{"openai/gpt-5.6-luna": 20, "x-ai/grok-4.6": 10},
				ArmIndices: map[string]rosterdata.ArmIndices{
					"openai/gpt-5.6-luna": {WII: 50, WPI: 0},
					"x-ai/grok-4.6":       {WII: 40, WPI: 1},
				},
			},
		},
	}
}

func newDistributionEngine(src admin.DistributionRosterSource) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v1/router/routing-distribution", admin.RoutingDistributionHandler(src))
	return engine
}

func getDistribution(engine *gin.Engine, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/router/routing-distribution"+query, nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func distributionModels(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var got struct {
		Points []router.DistributionPoint `json:"points"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	seen := map[string]struct{}{}
	var models []string
	for _, point := range got.Points {
		for _, share := range point.Models {
			if _, dup := seen[share.Model]; !dup {
				seen[share.Model] = struct{}{}
				models = append(models, share.Model)
			}
		}
	}
	return models
}

func TestRoutingDistributionHandler_ProjectsHMMRosterGrid(t *testing.T) {
	src := &fakeDistributionRoster{roster: twoArmDistributionRoster()}
	engine := newDistributionEngine(src)

	for _, strategy := range []string{"", "hmm", "hmm_embedding"} {
		rec := getDistribution(engine, "?grid=3&strategy="+strategy)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got struct {
			Points []router.DistributionPoint `json:"points"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Len(t, got.Points, 3, "grid query param sets the number of dial positions")
	}
	assert.Equal(t, []router.Strategy{router.StrategyFromContext(context.Background()), router.StrategyHMM, router.StrategyHMMEmbedding}, src.strategies,
		"the requested strategy selects which policy roster is projected")
}

func TestRoutingDistributionHandler_ExclusionsNarrowThePool(t *testing.T) {
	engine := newDistributionEngine(&fakeDistributionRoster{roster: twoArmDistributionRoster()})

	rec := getDistribution(engine, "?grid=2&excluded_models=%20gpt-5.6-luna%20,")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"grok-4.6"}, distributionModels(t, rec), "trimmed CSV exclusions must reach the projection")

	rec = getDistribution(engine, "?grid=2&excluded_providers=openai")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"grok-4.6"}, distributionModels(t, rec), "provider exclusions must reach the projection")

	rec = getDistribution(engine, "?grid=2&excluded_models=gpt-5.6-luna,grok-4.6")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "exclusions that empty the pool are a client config error")
}

func TestRoutingDistributionHandler_RejectsBadGrid(t *testing.T) {
	src := &fakeDistributionRoster{roster: twoArmDistributionRoster()}
	engine := newDistributionEngine(src)
	for _, bad := range []string{"1", "0", "-5", "abc", "1000"} {
		rec := getDistribution(engine, "?grid="+bad)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "grid=%q should be rejected", bad)
	}
	assert.Empty(t, src.strategies)
}

func TestRoutingDistributionHandler_RejectsNonHMMStrategy(t *testing.T) {
	src := &fakeDistributionRoster{roster: twoArmDistributionRoster()}
	engine := newDistributionEngine(src)
	for _, strategy := range []router.Strategy{router.Strategy("cluster"), router.StrategyRL, router.StrategyHMMBeta, "unknown"} {
		rec := getDistribution(engine, "?strategy="+string(strategy))
		assert.Equal(t, http.StatusBadRequest, rec.Code, strategy)
	}
	assert.Empty(t, src.strategies, "no legacy projection may stand in for a non-HMM strategy")
}

func TestRoutingDistributionHandler_SourceErrorIs503(t *testing.T) {
	engine := newDistributionEngine(&fakeDistributionRoster{err: errors.New("no active policy")})
	rec := getDistribution(engine, "")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
