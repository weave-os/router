package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability/otel"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionWinnerTelemetryPreservesOwnerAndAlternative(t *testing.T) {
	ctx := WithManagedSubscriptionUsage(context.Background())
	winner := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	winner.Served = true
	winner.SubscriptionAccountID = "00000000-0000-0000-0000-000000000101"
	winner.SubscriptionOwnerID = "00000000-0000-0000-0000-000000000102"
	winner.SubscriptionTier = auth.SubscriptionTierShared
	winner.IntendedModel = "claude-sonnet-4-6"
	telemetry := InsertTelemetryParams{RequestedModel: "auto", DecisionModel: "gpt-5.4"}
	applySubscriberTelemetry(ctx, &telemetry)
	assert.Equal(t, "claude-sonnet", telemetry.IntendedModelFamily)
	assert.Equal(t, "gpt", telemetry.FinalModelFamily)
	assert.Equal(t, "00000000-0000-0000-0000-000000000101", telemetry.SubscriptionAccountID)
	assert.Equal(t, "00000000-0000-0000-0000-000000000102", telemetry.SubscriptionOwnerID)
	assert.Equal(t, auth.SubscriptionTierShared, telemetry.SubscriptionTier)

	attrs := otel.NewAttrBuilder(6)
	(&Service{}).applySubscriptionSpanTelemetry(ctx, attrs, "auto", "gpt-5.4")
	values := make(map[string]string)
	for _, attr := range attrs.Build() {
		values[attr.Key] = attr.Value.GetStringValue()
	}
	assert.Equal(t, "00000000-0000-0000-0000-000000000101", values["subscription.account_id"])
	assert.Equal(t, "00000000-0000-0000-0000-000000000102", values["subscription.owner_id"])
	assert.Equal(t, "shared", values["subscription.tier"])
	assert.Equal(t, "claude-sonnet", values["model.intended_family"])
	assert.Equal(t, "gpt", values["model.final_family"])
}

func TestFailedAPIFallbackReplacesInboundSubscriptionAttribution(t *testing.T) {
	ctx := WithManagedSubscriptionUsage(context.Background())
	usage := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	usage.SubscriptionAttempted = true
	ctx = context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte("oauth-sentinel"),
		OAuth:  true,
		Source: credSourceSubscription,
	})
	apiCtx := context.WithValue(ctx, CredentialsContextKey{}, &Credentials{
		APIKey: []byte("api-sentinel"),
		Source: credSourceBYOK,
	})

	recordWinningCredentials(ctx, apiCtx)

	require.Same(t, CredentialsFromContext(apiCtx), CredentialsFromContext(ctx))
	winner := CredentialsFromContext(ctx)
	require.Equal(t, credSourceBYOK, winner.Source)
	require.Equal(t, []byte("api-sentinel"), winner.APIKey)
	require.False(t, winner.OAuth)
	assert.False(t, (&Service{}).costNeutralSubscriptionServed(ctx))
	telemetry := InsertTelemetryParams{RequestedModel: "claude-sonnet-5", DecisionModel: "claude-sonnet-5"}
	applySubscriberTelemetry(ctx, &telemetry)
	assert.Empty(t, telemetry.SubscriptionAccountID)
	assert.Empty(t, telemetry.SubscriptionOwnerID)
	assert.Empty(t, telemetry.SubscriptionTier)
}

func TestSubscriptionWinnerTelemetryDoesNotAttributeUnservedOrPaidCapacity(t *testing.T) {
	for _, served := range []bool{false, true} {
		ctx := WithManagedSubscriptionUsage(context.Background())
		winner := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
		winner.Served = served
		winner.OverageInUse = served
		winner.SubscriptionAccountID = "00000000-0000-0000-0000-000000000101"
		winner.SubscriptionOwnerID = "00000000-0000-0000-0000-000000000102"
		winner.SubscriptionTier = auth.SubscriptionTierShared
		telemetry := InsertTelemetryParams{RequestedModel: "unknown", DecisionModel: "unknown"}
		applySubscriberTelemetry(ctx, &telemetry)
		assert.Empty(t, telemetry.SubscriptionAccountID)
		assert.Empty(t, telemetry.SubscriptionOwnerID)
		assert.Empty(t, telemetry.SubscriptionTier)
		assert.Empty(t, telemetry.IntendedModelFamily)
		assert.Empty(t, telemetry.FinalModelFamily)
	}
}
