package analytics_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	analyticsapi "weave-os/router/internal/api/analytics"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func serve(handler gin.HandlerFunc, path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET(path, handler)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSchemaHandlerDescribesTheExport(t *testing.T) {
	rec := serve(analyticsapi.SchemaHandler(), "/v1/analytics/schema")

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Version string `json:"version"`
		Fields  []struct {
			Name string `json:"name"`
		} `json:"fields"`
		Notes struct {
			Grain          string `json:"grain"`
			HoldbackSecs   int    `json:"holdback_seconds"`
			DecisionCaveat string `json:"decision_reason_caveat"`
		} `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Version)
	require.NotEmpty(t, body.Fields)
	// The grain note is the one thing a consumer gets wrong by default: rows
	// are upstream actions, so counting them over-counts requests.
	require.Contains(t, body.Notes.Grain, "upstream action")
	require.Positive(t, body.Notes.HoldbackSecs)
	require.NotEmpty(t, body.Notes.DecisionCaveat)
}

// The price book is what lets a consumer re-derive the cost columns instead of
// trusting the router's savings math.
func TestModelsHandlerPublishesPrices(t *testing.T) {
	rec := serve(analyticsapi.ModelsHandler(), "/v1/analytics/models")

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Models []struct {
			ID            string `json:"id"`
			ContextWindow int    `json:"context_window"`
			Providers     []struct {
				Provider             string  `json:"provider"`
				InputUSDPer1M        float64 `json:"input_usd_per_1m"`
				OutputUSDPer1M       float64 `json:"output_usd_per_1m"`
				CacheWriteMultiplier float64 `json:"cache_write_multiplier"`
				CacheReadMultiplier  float64 `json:"cache_read_multiplier"`
				LongContext          *struct {
					ThresholdTokens      int     `json:"threshold_tokens"`
					InputUSDPer1M        float64 `json:"input_usd_per_1m"`
					OutputUSDPer1M       float64 `json:"output_usd_per_1m"`
					CacheWriteMultiplier float64 `json:"cache_write_multiplier"`
					CacheReadMultiplier  float64 `json:"cache_read_multiplier"`
				} `json:"long_context"`
			} `json:"providers"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Models)
	for _, m := range body.Models {
		require.NotEmpty(t, m.ID)
		require.Positive(t, m.ContextWindow)
		require.NotEmpty(t, m.Providers, "%s has no provider bindings", m.ID)
		for _, p := range m.Providers {
			require.NotEmpty(t, p.Provider)
			require.Positive(t, p.CacheWriteMultiplier, "%s/%s must publish a usable cache write multiplier", m.ID, p.Provider)
			require.Positive(t, p.CacheReadMultiplier, "%s/%s must publish a usable cache multiplier", m.ID, p.Provider)
			if p.LongContext != nil {
				require.Positive(t, p.LongContext.ThresholdTokens)
				require.Positive(t, p.LongContext.InputUSDPer1M)
				require.Positive(t, p.LongContext.OutputUSDPer1M)
				require.Positive(t, p.LongContext.CacheWriteMultiplier)
				require.Positive(t, p.LongContext.CacheReadMultiplier)
			}
		}
	}
}
