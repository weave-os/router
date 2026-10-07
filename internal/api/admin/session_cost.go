package admin

import (
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// usdMicrosPerUSD converts micros to USD for display only; applied at encoding so float rounding never accumulates.
const usdMicrosPerUSD = 1_000_000.0

// sessionCostResponse is the JSON envelope for GET /v1/sessions/:id/cost: the
// Weave public API's RouterSessionCost plus the savings fields the Codex status
// hook reads. The *_usd_micros integers are authoritative; decimal fields are
// derived for display only.
type sessionCostResponse struct {
	ContextSnapshot        *proxy.ContextSnapshot `json:"context_snapshot,omitempty"`
	SessionID              string                 `json:"session_id"`
	RequestCount           int64                  `json:"request_count"`
	ActualCostUSDMicros    int64                  `json:"actual_cost_usd_micros"`
	ActualCostUSD          float64                `json:"actual_cost_usd"`
	RequestedCostUSDMicros int64                  `json:"requested_cost_usd_micros"`
	RequestedCostUSD       float64                `json:"requested_cost_usd"`
	SavingsUSDMicros       int64                  `json:"savings_usd_micros"`
	SavingsUSD             float64                `json:"savings_usd"`
	InputTokens            int64                  `json:"input_tokens"`
	OutputTokens           int64                  `json:"output_tokens"`
	CacheCreationTokens    int64                  `json:"cache_creation_tokens"`
	CacheReadTokens        int64                  `json:"cache_read_tokens"`
	LastRecordedAt         string                 `json:"last_recorded_at"`
}

// SessionCostHandler returns committed router cost for a session, scoped to the
// installation that owns the presented rk_ or ra_ key. Exists so the Codex status
// hook gets real savings without a local price table it cannot keep in sync.
// Errors use the Weave public API's message/description shape.
func SessionCostHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation := middleware.InstallationFrom(c)
		if installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "Unauthorized"})
			return
		}

		cost, err := proxySvc.SessionCost(c.Request.Context(), installation.ID, c.Param("session_id"))
		if errors.Is(err, proxy.ErrInvalidSessionID) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": "Invalid session id", "description": err.Error()})
			return
		}
		// One response for unknown, foreign, and not-yet-committed sessions:
		// distinguishing them would confirm a foreign session's existence.
		if errors.Is(err, proxy.ErrSessionCostNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"message": "No router cost found for this session. It may not exist, or its telemetry may not be recorded yet."})
			return
		}
		if errors.Is(err, proxy.ErrSessionCostUnavailable) {
			observability.FromGin(c).Warn("Session cost requested but telemetry storage is not configured", "installation_id", installation.ID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": "WeaveRouter is not available on this deployment"})
			return
		}
		if err != nil {
			observability.FromGin(c).Error("Failed to fetch session cost", "installation_id", installation.ID, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"message": "Failed to retrieve router session cost"})
			return
		}

		if cost.ContextSnapshot != nil && !cost.ContextSnapshot.Fresh(time.Now()) {
			cost.ContextSnapshot = nil
		}
		c.Header("Cache-Control", "no-store")
		savings := cost.RequestedCostUSDMicros - cost.ActualCostUSDMicros
		c.JSON(http.StatusOK, sessionCostResponse{
			SessionID:              cost.SessionID,
			ContextSnapshot:        cost.ContextSnapshot,
			RequestCount:           cost.RequestCount,
			ActualCostUSDMicros:    cost.ActualCostUSDMicros,
			ActualCostUSD:          float64(cost.ActualCostUSDMicros) / usdMicrosPerUSD,
			RequestedCostUSDMicros: cost.RequestedCostUSDMicros,
			RequestedCostUSD:       float64(cost.RequestedCostUSDMicros) / usdMicrosPerUSD,
			SavingsUSDMicros:       savings,
			SavingsUSD:             float64(savings) / usdMicrosPerUSD,
			InputTokens:            cost.InputTokens,
			OutputTokens:           cost.OutputTokens,
			CacheCreationTokens:    cost.CacheCreationTokens,
			CacheReadTokens:        cost.CacheReadTokens,
			LastRecordedAt:         cost.LastRecordedAt.UTC().Format(time.RFC3339Nano),
		})
	}
}
