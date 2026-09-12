package postgres

import (
	"testing"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPinUsageParamsPreservesCompletedTurnIdentity(t *testing.T) {
	completedAt := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	params := sessionPinUsageParams([sessionpin.SessionKeyLen]byte{1, 2}, sessionpin.DefaultRole, sessionpin.Usage{
		Strategy:           router.StrategyHMM,
		PreserveUsage:      true,
		CompletedRequestID: "request-complete",
		CompletedRouteID:   "route-complete",
		CompletedModel:     "claude-sonnet-4-6",
		CompletedStrategy:  router.StrategyHMM,
		CompletedAt:        completedAt,
	})

	assert.True(t, params.PreserveUsage)
	assert.Equal(t, "request-complete", params.CompletedRequestID)
	assert.Equal(t, "route-complete", params.CompletedRouteID)
	assert.Equal(t, "claude-sonnet-4-6", params.CompletedModel)
	assert.Equal(t, string(router.StrategyHMM), params.CompletedStrategy)
	require.True(t, params.CompletedAt.Valid)
	assert.Equal(t, completedAt, params.CompletedAt.Time)
}

func TestToSessionPinMapsCompletedTurnIdentity(t *testing.T) {
	completedAt := time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)
	row := sqlc.RouterSessionPin{
		LastCompletedRequestID: "request-complete",
		LastCompletedRouteID:   "route-complete",
		LastCompletedModel:     "claude-sonnet-4-6",
		LastCompletedStrategy:  string(router.StrategyHMM),
		LastCompletedAt:        pgtype.Timestamptz{Time: completedAt, Valid: true},
	}

	pin := toSessionPin(row)
	assert.Equal(t, "request-complete", pin.LastCompletedRequestID)
	assert.Equal(t, "route-complete", pin.LastCompletedRouteID)
	assert.Equal(t, "claude-sonnet-4-6", pin.LastCompletedModel)
	assert.Equal(t, router.StrategyHMM, pin.LastCompletedStrategy)
	assert.Equal(t, completedAt, pin.LastCompletedAt)
}
