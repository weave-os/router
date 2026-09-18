package postgres

import (
	"testing"
	"time"

	"weave-os/router/internal/sqlc"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
)

func TestToSessionPinOutputLimitMarker(t *testing.T) {
	t.Parallel()

	endedAt := time.Date(2026, 9, 14, 12, 30, 0, 0, time.UTC)

	t.Run("null marker is zero, never inferred from output tokens", func(t *testing.T) {
		t.Parallel()
		pin := toSessionPin(sqlc.RouterSessionPin{
			LastOutputTokens: 64000,
			LastTurnEndedAt:  pgtype.Timestamptz{Time: endedAt, Valid: true},
		})
		assert.True(t, pin.LastOutputLimitAt.IsZero())
		assert.Equal(t, endedAt, pin.LastTurnEndedAt)
		assert.Equal(t, 64000, pin.LastOutputTokens)
	})

	t.Run("confirmed cap round-trips as the turn's ended instant", func(t *testing.T) {
		t.Parallel()
		pin := toSessionPin(sqlc.RouterSessionPin{
			LastTurnEndedAt:   pgtype.Timestamptz{Time: endedAt, Valid: true},
			LastOutputLimitAt: pgtype.Timestamptz{Time: endedAt, Valid: true},
		})
		assert.Equal(t, endedAt, pin.LastOutputLimitAt)
		assert.True(t, pin.LastOutputLimitAt.Equal(pin.LastTurnEndedAt))
	})

	t.Run("stale marker keeps its own instant so a reader can reject it", func(t *testing.T) {
		t.Parallel()
		stale := endedAt.Add(-time.Hour)
		pin := toSessionPin(sqlc.RouterSessionPin{
			LastTurnEndedAt:   pgtype.Timestamptz{Time: endedAt, Valid: true},
			LastOutputLimitAt: pgtype.Timestamptz{Time: stale, Valid: true},
		})
		assert.Equal(t, stale, pin.LastOutputLimitAt)
		assert.False(t, pin.LastOutputLimitAt.Equal(pin.LastTurnEndedAt))
	})

	t.Run("marker without a turn timestamp is not coerced", func(t *testing.T) {
		t.Parallel()
		pin := toSessionPin(sqlc.RouterSessionPin{
			LastOutputLimitAt: pgtype.Timestamptz{Time: endedAt, Valid: true},
		})
		assert.True(t, pin.LastTurnEndedAt.IsZero())
		assert.Equal(t, endedAt, pin.LastOutputLimitAt)
	})
}

func TestToSessionPinDemotionCooldowns(t *testing.T) {
	t.Parallel()

	until := time.Date(2026, 9, 18, 12, 0, 45, 0, time.UTC)

	t.Run("decodes model to instant map", func(t *testing.T) {
		t.Parallel()
		pin := toSessionPin(sqlc.RouterSessionPin{
			DemotionCooldowns: []byte(`{"claude-opus-4-7":"2026-09-18T12:00:45Z"}`),
		})
		assert.Equal(t, map[string]time.Time{"claude-opus-4-7": until}, pin.DemotionCooldowns)
	})

	t.Run("empty object and NULL are no cooldowns", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, toSessionPin(sqlc.RouterSessionPin{DemotionCooldowns: []byte(`{}`)}).DemotionCooldowns)
		assert.Nil(t, toSessionPin(sqlc.RouterSessionPin{}).DemotionCooldowns)
	})

	t.Run("unreadable value fails open to no cooldowns", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, toSessionPin(sqlc.RouterSessionPin{DemotionCooldowns: []byte(`[1,2]`)}).DemotionCooldowns)
		assert.Nil(t, toSessionPin(sqlc.RouterSessionPin{DemotionCooldowns: []byte(`{"m":"soon"}`)}).DemotionCooldowns)
	})
}
