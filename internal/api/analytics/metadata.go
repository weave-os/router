package analytics

import (
	"net/http"

	"weave-os/router/internal/analytics"

	"github.com/gin-gonic/gin"
)

type schemaResponse struct {
	Version string             `json:"version"`
	Fields  []analytics.Field  `json:"fields"`
	Notes   schemaResponseNote `json:"notes"`
}

// schemaResponseNote carries semantics a consumer cannot infer from the field list.
type schemaResponseNote struct {
	Grain          string `json:"grain"`
	Idempotency    string `json:"idempotency"`
	Ordering       string `json:"ordering"`
	HoldbackSecs   int    `json:"holdback_seconds"`
	MaxPageLimit   int    `json:"max_page_limit"`
	DefaultLimit   int    `json:"default_page_limit"`
	DecisionCaveat string `json:"decision_reason_caveat"`
}

// SchemaHandler serves GET /v1/analytics/schema: the machine-readable field
// dictionary so a consumer can generate warehouse DDL.
func SchemaHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, schemaResponse{
			Version: analytics.SchemaVersion,
			Fields:  analytics.Schema(),
			Notes: schemaResponseNote{
				Grain:          "One row per upstream action, not per user-visible request. Retries, failovers, compaction, title generation, classifier calls and sub-agent turns each emit their own row.",
				Idempotency:    "Rows are written once and never updated. Deduplicate on id; a replayed page is a safe no-op merge.",
				Ordering:       "Ascending by recorded_at (ingest time), then id. requested_at (event time) is not monotonic within a page.",
				HoldbackSecs:   int(analytics.Holdback.Seconds()),
				MaxPageLimit:   analytics.MaxLimit,
				DefaultLimit:   analytics.DefaultLimit,
				DecisionCaveat: "decision_reason is free-form diagnostic text whose format changes between router versions. Do not parse it.",
			},
		})
	}
}

type modelsResponse struct {
	Models []analytics.ModelPrice `json:"models"`
}

// ModelsHandler serves GET /v1/analytics/models: the current price book so a
// consumer can recompute cost and savings columns independently.
func ModelsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, modelsResponse{Models: analytics.PriceBook()})
	}
}
