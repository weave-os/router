package postgres

import (
	"testing"
	"time"

	"workweave/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecisionFromExportRowMapsServedCosts(t *testing.T) {
	actualIn := int64(1_000_000)
	actualOut := int64(250_000)

	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{
		ActualInputCostUsd:  &actualIn,
		ActualOutputCostUsd: &actualOut,
	})

	require.NotNil(t, got.ActualInputCostUSD)
	assert.InDelta(t, 1.0, *got.ActualInputCostUSD, 1e-9)
	assert.InDelta(t, 0.25, *got.ActualOutputCostUSD, 1e-9)
}

// A row with no cost data exports nulls rather than a fabricated $0 that a
// consumer would average into its spend number.
func TestDecisionFromExportRowUnpricedRowHasNullCosts(t *testing.T) {
	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{})

	assert.Nil(t, got.ActualInputCostUSD)
	assert.Nil(t, got.ActualOutputCostUSD)
	assert.Nil(t, got.InputTokens)
}

// Absent booleans are "did not happen", so they export as false rather than
// forcing every consumer to handle a three-valued flag.
func TestDecisionFromExportRowNullBooleansAreFalse(t *testing.T) {
	sticky := true

	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{StickyHit: &sticky})

	assert.True(t, got.StickyHit)
	assert.False(t, got.FailoverUsed)
	assert.False(t, got.CrossFormat)
}

func TestDecisionFromExportRowMapsIdentity(t *testing.T) {
	rowID := uuid.New()
	userID := uuid.New()
	accountID := uuid.New()
	recordedAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{
		ID:              rowID,
		CreatedAt:       pgtype.Timestamptz{Time: recordedAt, Valid: true},
		Timestamp:       pgtype.Timestamptz{Time: recordedAt.Add(-time.Second), Valid: true},
		RequestID:       "req-1",
		RouterUserID:    pgtype.UUID{Bytes: userID, Valid: true},
		UserAccountUUID: pgtype.UUID{Bytes: accountID, Valid: true},
	})

	assert.Equal(t, rowID.String(), got.ID)
	assert.True(t, got.RecordedAt.Equal(recordedAt))
	assert.Equal(t, "req-1", got.RequestID)
	require.NotNil(t, got.UserID)
	assert.Equal(t, userID.String(), *got.UserID)
	require.NotNil(t, got.UserAccountUUID)
	assert.Equal(t, accountID.String(), *got.UserAccountUUID)
}

// A NULL user id must not surface as the all-zeroes uuid, which would join to
// a nonexistent user in the warehouse.
func TestDecisionFromExportRowNullUserIsNull(t *testing.T) {
	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{})

	assert.Nil(t, got.UserID)
	assert.Nil(t, got.UserAccountUUID)
}
