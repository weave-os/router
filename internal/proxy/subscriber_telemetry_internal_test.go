package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplySubscriberTelemetryIncludedUsage(t *testing.T) {
	ctx := entitlement.WithProductScope(context.Background(), entitlement.PlanBoost)
	ctx = entitlement.WithCoverage(ctx, entitlement.Coverage{
		EntitlementVersion: 17,
		Plan:               entitlement.PlanBoost,
	})
	ctx = requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{
		ProfileKey:      "boost-default",
		ProfileRevision: "profile-sha",
		ReleaseID:       "release-id",
		BindingID:       "binding-id",
	})
	telemetry := InsertTelemetryParams{
		ActualInputCostUSD:  0.0000015,
		ActualOutputCostUSD: 0.0000025,
		InputTokens:         1,
	}

	applySubscriberTelemetry(ctx, &telemetry)

	require.NotNil(t, telemetry.EntitlementVersion)
	assert.Equal(t, int64(17), *telemetry.EntitlementVersion)
	assert.Equal(t, string(entitlement.PlanBoost), telemetry.SubscriberPlan)
	assert.Equal(t, string(entitlement.CapacitySourceIncludedRouter), telemetry.CapacitySource)
	require.NotNil(t, telemetry.RetailUsageMicros)
	assert.Equal(t, int64(4), *telemetry.RetailUsageMicros)
	require.NotNil(t, telemetry.IncludedUsageMicros)
	assert.Equal(t, int64(4), *telemetry.IncludedUsageMicros)
	assert.Nil(t, telemetry.LinkedUsageMicros)
	assert.Nil(t, telemetry.PrepaidUsageMicros)
	assert.Equal(t, "boost-default", telemetry.ServingProfileID)
	assert.Equal(t, "profile-sha", telemetry.ServingProfileVersion)
	assert.Equal(t, "release-id", telemetry.ServingReleaseID)
	assert.Equal(t, "binding-id", telemetry.ServingBindingID)
	assert.Equal(t, boostSourceOptimizerVersion, telemetry.BoostOptimizerVersion)
}

func TestApplySubscriberTelemetryCapacitySources(t *testing.T) {
	prepaid := billing.PrepaidAuthorization{CapacitySource: entitlement.CapacitySourcePrepaid}
	tests := []struct {
		name             string
		ctx              context.Context
		credentialSource string
		want             entitlement.CapacitySource
	}{
		{
			name:             "linked claude",
			ctx:              entitlement.WithProductScope(context.Background(), entitlement.PlanBoost),
			credentialSource: credSourceSubscription,
			want:             entitlement.CapacitySourceLinkedClaude,
		},
		{
			name:             "linked codex",
			ctx:              entitlement.WithProductScope(context.Background(), entitlement.PlanBoost),
			credentialSource: credSourceCodexSubscription,
			want:             entitlement.CapacitySourceLinkedCodex,
		},
		{
			name: "prepaid",
			ctx:  billing.WithPrepaidAuthorization(context.Background(), prepaid),
			want: entitlement.CapacitySourcePrepaid,
		},
		{
			name: "billing override",
			ctx:  context.WithValue(context.Background(), billing.HasOverrideContextKey, true),
			want: entitlement.CapacitySourceBillingOverride,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			telemetry := InsertTelemetryParams{
				ActualInputCostUSD:  0.25,
				ActualOutputCostUSD: 0.75,
				CredentialSource:    tt.credentialSource,
				InputTokens:         1,
			}

			applySubscriberTelemetry(tt.ctx, &telemetry)

			assert.Equal(t, string(tt.want), telemetry.CapacitySource)
			require.NotNil(t, telemetry.RetailUsageMicros)
			assert.Equal(t, int64(1_000_000), *telemetry.RetailUsageMicros)
			switch tt.want {
			case entitlement.CapacitySourceLinkedClaude, entitlement.CapacitySourceLinkedCodex:
				require.NotNil(t, telemetry.LinkedUsageMicros)
				assert.Equal(t, int64(1_000_000), *telemetry.LinkedUsageMicros)
			case entitlement.CapacitySourcePrepaid:
				require.NotNil(t, telemetry.PrepaidUsageMicros)
				assert.Equal(t, int64(1_000_000), *telemetry.PrepaidUsageMicros)
			default:
				assert.Nil(t, telemetry.IncludedUsageMicros)
				assert.Nil(t, telemetry.LinkedUsageMicros)
				assert.Nil(t, telemetry.PrepaidUsageMicros)
			}
		})
	}
}

func TestApplySubscriberSettlementTelemetry(t *testing.T) {
	includedContext := entitlement.WithCoverage(context.Background(), entitlement.Coverage{})
	entitlement.MarkSettlementFailed(includedContext)
	included := InsertTelemetryParams{CapacitySource: string(entitlement.CapacitySourceIncludedRouter)}
	applySubscriberSettlementTelemetry(captureSubscriberSettlementState(includedContext), &included)
	require.NotNil(t, included.SettlementFailed)
	assert.True(t, *included.SettlementFailed)

	prepaidContext := billing.WithPrepaidAuthorization(context.Background(), billing.PrepaidAuthorization{})
	billing.MarkPrepaidSettlementFailed(prepaidContext)
	prepaid := InsertTelemetryParams{CapacitySource: string(entitlement.CapacitySourcePrepaid)}
	applySubscriberSettlementTelemetry(captureSubscriberSettlementState(prepaidContext), &prepaid)
	require.NotNil(t, prepaid.SettlementFailed)
	assert.True(t, *prepaid.SettlementFailed)

	linked := InsertTelemetryParams{CapacitySource: string(entitlement.CapacitySourceLinkedClaude)}
	applySubscriberSettlementTelemetry(subscriberSettlementState{}, &linked)
	assert.Nil(t, linked.SettlementFailed)
}

func TestApplySubscriberSettlementTelemetryUsesCapturedState(t *testing.T) {
	ctx := entitlement.WithCoverage(context.Background(), entitlement.Coverage{})
	state := captureSubscriberSettlementState(ctx)
	entitlement.MarkSettlementFailed(ctx)

	telemetry := InsertTelemetryParams{
		CapacitySource: string(entitlement.CapacitySourceIncludedRouter),
	}
	applySubscriberSettlementTelemetry(state, &telemetry)

	require.NotNil(t, telemetry.SettlementFailed)
	assert.False(t, *telemetry.SettlementFailed)
}

func TestApplySubscriberTelemetryLeavesBYOKAndUnknownUsageUnattributed(t *testing.T) {
	ctx := entitlement.WithCoverage(context.Background(), entitlement.Coverage{
		EntitlementVersion: 17,
		Plan:               entitlement.PlanMax,
	})
	for _, telemetry := range []InsertTelemetryParams{
		{
			CredentialSource:   credSourceBYOK,
			InputTokens:        1,
			ActualInputCostUSD: 0.25,
		},
		{},
	} {
		applySubscriberTelemetry(ctx, &telemetry)
		assert.Empty(t, telemetry.CapacitySource)
		assert.Nil(t, telemetry.RetailUsageMicros)
		assert.Nil(t, telemetry.IncludedUsageMicros)
	}
}

func TestApplySubscriberTelemetryLeavesUsageEmptyWithoutCapacitySource(t *testing.T) {
	telemetry := InsertTelemetryParams{
		ActualInputCostUSD:  0.25,
		ActualOutputCostUSD: 0.75,
		CredentialSource:    credSourceSubscription,
	}

	applySubscriberTelemetry(context.Background(), &telemetry)

	assert.Empty(t, telemetry.CapacitySource)
	assert.Nil(t, telemetry.RetailUsageMicros)
	assert.Nil(t, telemetry.IncludedUsageMicros)
	assert.Nil(t, telemetry.LinkedUsageMicros)
	assert.Nil(t, telemetry.PrepaidUsageMicros)
}
