package admin

import (
	"errors"
	"net/http"

	"workweave/router/internal/proxy"
	"workweave/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// usdMicrosPerUSD converts micros to USD for display only; applied at encoding so float rounding never accumulates.
const usdMicrosPerUSD = 1_000_000.0

// sessionCostResponse is the JSON envelope for GET /v1/sessions/:id/cost.
// The *_usd_micros integers are authoritative; decimal fields are derived for display only.
type sessionCostResponse struct {
	SessionID              string  `json:"session_id"`
	RequestCount           int64   `json:"request_count"`
	ActualCostUSDMicros    int64   `json:"actual_cost_usd_micros"`
	ActualCostUSD          float64 `json:"actual_cost_usd"`
	RequestedCostUSDMicros int64   `json:"requested_cost_usd_micros"`
	RequestedCostUSD       float64 `json:"requested_cost_usd"`
	SavingsUSDMicros       int64   `json:"savings_usd_micros"`
	SavingsUSD             float64 `json:"savings_usd"`
	InputTokens            int64   `json:"input_tokens"`
	OutputTokens           int64   `json:"output_tokens"`
	CacheCreationTokens    int64   `json:"cache_creation_tokens"`
	CacheReadTokens        int64   `json:"cache_read_tokens"`
	LastRecordedAt         string  `json:"last_recorded_at"`
}

// SessionCostHandler returns committed router cost for a session, scoped to the rk_ key's installation.
// Exists so the Codex status hook gets real savings without a local price table it cannot keep in sync.
func SessionCostHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation := middleware.InstallationFrom(c)
		if installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}

		cost, err := proxySvc.SessionCost(c.Request.Context(), installation.ID, c.Param("session_id"))
		// One response for unknown, foreign, and not-yet-committed sessions:
		// distinguishing them would confirm a foreign session's existence.
		if errors.Is(err, proxy.ErrSessionCostNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "session_cost_not_found"})
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch session cost."})
			return
		}

		savings := cost.RequestedCostUSDMicros - cost.ActualCostUSDMicros
		c.JSON(http.StatusOK, sessionCostResponse{
			SessionID:              cost.SessionID,
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
			LastRecordedAt:         cost.LastRecordedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
}
