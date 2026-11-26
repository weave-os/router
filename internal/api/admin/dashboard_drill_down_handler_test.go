package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type drillDownRepo struct {
	stubTelemetryRepo
	rows []turnrecord.TelemetryDrillDownRow
}

func (r drillDownRepo) GetTelemetryDashboardDrillDown(context.Context, string, time.Time, time.Time) ([]turnrecord.TelemetryDrillDownRow, error) {
	return r.rows, nil
}

func TestMetricsDrillDownNamesTheComparisonModelsItUsed(t *testing.T) {
	automatic := turnrecord.TelemetryDrillDownRow{PersonID: "person", SessionID: "session", TelemetryDashboardFact: turnrecord.TelemetryDashboardFact{DecisionModel: "claude-haiku-5-5", DecisionType: "router_pick", RequestCount: 1, InputTokens: 1_000_000, ActualCostUSD: 1}}
	efficient := automatic
	efficient.ComparisonRoutingModel = string(router.ModelAutoEfficient)

	for _, tc := range []struct {
		name          string
		rows          []turnrecord.TelemetryDrillDownRow
		wantModel     string
		wantBreakdown int
	}{
		{name: "efficient only", rows: []turnrecord.TelemetryDrillDownRow{efficient}, wantModel: "Claude Sonnet 5.5", wantBreakdown: 1},
		{name: "mixed", rows: []turnrecord.TelemetryDrillDownRow{automatic, efficient}, wantModel: "mixed (Claude Opus 5.5 / Claude Sonnet 5.5)", wantBreakdown: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.GET("/drill-down", func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{ID: "installation"})
			}, admin.MetricsDrillDownHandler(turnrecord.Queries{Repo: drillDownRepo{rows: tc.rows}}))
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/drill-down", nil))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

			var body struct {
				ComparisonModel  string `json:"comparison_model"`
				ComparisonModels []struct {
					ComparisonModel string  `json:"comparison_model"`
					SavingsUSD      float64 `json:"savings_usd"`
				} `json:"comparison_models"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.Equal(t, tc.wantModel, body.ComparisonModel)
			require.Len(t, body.ComparisonModels, tc.wantBreakdown)
		})
	}
}
