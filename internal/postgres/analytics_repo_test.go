package postgres

import (
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"

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

// A subscription-served turn is paid for by the caller's own quota, so it
// exports $0 against real token counts instead of the catalog rate.
func TestDecisionFromExportRowSubscriptionServedCostsAreZero(t *testing.T) {
	actualIn := int64(1_000_000)
	actualOut := int64(250_000)
	inputTokens := int32(4_200)

	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{
		SubscriptionServed:  true,
		ActualInputCostUsd:  &actualIn,
		ActualOutputCostUsd: &actualOut,
		InputTokens:         &inputTokens,
	})

	assert.True(t, got.SubscriptionServed)
	require.NotNil(t, got.ActualInputCostUSD)
	require.NotNil(t, got.ActualOutputCostUSD)
	assert.Zero(t, *got.ActualInputCostUSD)
	assert.Zero(t, *got.ActualOutputCostUSD)
	require.NotNil(t, got.InputTokens)
	assert.Equal(t, int64(4_200), *got.InputTokens)
}

// A turn that failed over off a spent subscription was paid for with a Weave or
// BYOK key, so it keeps its catalog price.
func TestDecisionFromExportRowFailoverOffSubscriptionKeepsCost(t *testing.T) {
	actualIn := int64(1_000_000)
	failover := true

	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{
		SubscriptionServed: false,
		FailoverUsed:       &failover,
		ActualInputCostUsd: &actualIn,
	})

	assert.False(t, got.SubscriptionServed)
	require.NotNil(t, got.ActualInputCostUSD)
	assert.InDelta(t, 1.0, *got.ActualInputCostUSD, 1e-9)
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

func TestDecisionFromExportRowMapsBlindExperimentFields(t *testing.T) {
	arm := string(auth.BlindExperimentArmPassthrough)
	assignmentSource := string(auth.BlindExperimentAssignmentManual)
	subjectKey := "account-1"

	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{
		BlindExperimentArm:              &arm,
		BlindExperimentAssignmentSource: &assignmentSource,
		BlindExperimentSubjectKey:       &subjectKey,
	})

	require.NotNil(t, got.BlindExperimentArm)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, *got.BlindExperimentArm)
	require.NotNil(t, got.BlindExperimentAssignmentSource)
	assert.Equal(t, auth.BlindExperimentAssignmentManual, *got.BlindExperimentAssignmentSource)
	require.NotNil(t, got.BlindExperimentSubjectKey)
	assert.Equal(t, subjectKey, *got.BlindExperimentSubjectKey)
}

func TestDecisionFromExportRowPreservesNullBlindExperimentFields(t *testing.T) {
	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{})

	assert.Nil(t, got.BlindExperimentArm)
	assert.Nil(t, got.BlindExperimentAssignmentSource)
	assert.Nil(t, got.BlindExperimentSubjectKey)
}

func TestDecisionFromExportRowMapsCohortAttribution(t *testing.T) {
	cohortID := uuid.New()
	groupID := int16(2)
	phaseIndex := int16(3)
	revision := int32(4)
	scheduledArm := string(auth.BlindExperimentArmPassthrough)
	applied := false
	reason := string(auth.CohortBypassHardPin)
	decision := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{
		CohortExperimentID:     pgtype.UUID{Bytes: cohortID, Valid: true},
		CohortGroupID:          &groupID,
		CohortPhaseIndex:       &phaseIndex,
		CohortRevision:         &revision,
		CohortScheduledArm:     &scheduledArm,
		CohortTreatmentApplied: &applied,
		CohortBypassReason:     &reason,
	})

	assert.Equal(t, cohortID.String(), *decision.CohortExperimentID)
	assert.Equal(t, int64(groupID), *decision.CohortGroupID)
	assert.Equal(t, int64(phaseIndex), *decision.CohortPhaseIndex)
	assert.Equal(t, int64(revision), *decision.CohortRevision)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, *decision.CohortScheduledArm)
	assert.False(t, *decision.CohortTreatmentApplied)
	assert.Equal(t, auth.CohortBypassHardPin, *decision.CohortBypassReason)

	empty := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{})
	assert.Nil(t, empty.CohortExperimentID)
	assert.Nil(t, empty.CohortGroupID)
	assert.Nil(t, empty.CohortScheduledArm)
	assert.Nil(t, empty.CohortTreatmentApplied)
}

func TestDecisionFromExportRowMapsBenchmarkAttribution(t *testing.T) {
	plan := "boost"
	capacitySource := "linked_claude"
	rolloutID := "benchmark-run-1"
	routeID := "route-1"
	retailUsage := int64(900)
	linkedUsage := int64(900)
	settlementFailed := false
	entitlementVersion := int64(12)

	got := decisionFromExportRow(sqlc.GetRoutingDecisionsForExportRow{
		RolloutID:            &rolloutID,
		RouteID:              &routeID,
		SubscriberPlan:       &plan,
		EntitlementVersion:   &entitlementVersion,
		CapacitySource:       &capacitySource,
		RetailUsageUsdMicros: &retailUsage,
		LinkedUsageUsdMicros: &linkedUsage,
		SettlementFailed:     &settlementFailed,
	})

	assert.Equal(t, &rolloutID, got.RolloutID)
	assert.Equal(t, &routeID, got.RouteID)
	assert.Equal(t, &plan, got.SubscriberPlan)
	assert.Equal(t, &entitlementVersion, got.EntitlementVersion)
	assert.Equal(t, &capacitySource, got.CapacitySource)
	assert.Equal(t, &retailUsage, got.RetailUsageUSDMicros)
	assert.Equal(t, &linkedUsage, got.LinkedUsageUSDMicros)
	assert.Equal(t, &settlementFailed, got.SettlementFailed)
	assert.Nil(t, got.IncludedUsageUSDMicros)
	assert.Nil(t, got.PrepaidUsageUSDMicros)
}
