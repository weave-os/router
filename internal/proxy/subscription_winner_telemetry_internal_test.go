package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnterpriseSeatTelemetryUsesTheServingCredential(t *testing.T) {
	enterprise := &Credentials{APIKey: []byte("synthetic-enterprise"), OAuth: true, Source: credSourceSubscription, SubscriptionPlan: providers.SubscriptionPlanEnterprise}
	team := &Credentials{APIKey: []byte("synthetic-team"), OAuth: true, Source: credSourceSubscription, SubscriptionPlan: providers.SubscriptionPlanTeam}
	unknown := &Credentials{APIKey: []byte("synthetic-unknown"), OAuth: true, Source: credSourceSubscription}
	codex := &Credentials{APIKey: []byte("synthetic-codex"), OAuth: true, Source: credSourceCodexSubscription}
	apiKey := &Credentials{APIKey: []byte("synthetic-key"), Source: credSourceBYOK}

	for _, tc := range []struct {
		name    string
		winner  *Credentials
		overage bool
		source  string
	}{
		{name: "included Enterprise", winner: enterprise, source: credSourceSubscription},
		{name: "Enterprise overage", winner: enterprise, overage: true, source: credSourceSubscriptionOverage},
		{name: "Team fallback", winner: team, source: credSourceSubscription},
		{name: "Unverified fallback", winner: unknown, source: credSourceSubscription},
		{name: "Codex fallback", winner: codex, source: credSourceCodexSubscription},
		{name: "API fallback", winner: apiKey, source: credSourceBYOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(credentials.WithManagedSubscriptionUsage(context.Background()), CredentialsContextKey{}, enterprise)
			ctx = context.WithValue(ctx, claudeSubscriptionPlanKey{}, providers.SubscriptionPlanEnterprise)
			winnerCtx := context.WithValue(ctx, CredentialsContextKey{}, tc.winner)
			recordWinningCredentials(ctx, winnerCtx)
			if tc.overage {
				usage := mustAs[*credentials.ManagedSubscriptionUsage](t, ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}))
				usage.Served = true
				usage.OverageInUse = true
				usage.CredentialSource = tc.winner.Source
			}

			attrs := telemetry.NewAttrBuilder(4)
			(&Service{}).applySubscriptionSpanTelemetry(ctx, attrs, "auto", "claude-sonnet-5")
			values := make(map[string]telemetry.AttrValue)
			for _, attr := range attrs.Build() {
				values[attr.Key] = attr.Value
			}
			assert.Equal(t, tc.source, values["credential.source"].GetStringValue())
			assert.Equal(t, tc.winner == enterprise, values["subscription.claude_enterprise"].GetBoolValue())
		})
	}
}

func TestSubscriptionWinnerTelemetryPreservesOwnerAndAlternative(t *testing.T) {
	ctx := credentials.WithManagedSubscriptionUsage(context.Background())
	winner := mustAs[*credentials.ManagedSubscriptionUsage](t, ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}))
	winner.Served = true
	winner.SubscriptionAccountID = "00000000-0000-0000-0000-000000000101"
	winner.SubscriptionOwnerID = "00000000-0000-0000-0000-000000000102"
	winner.SubscriptionTier = auth.SubscriptionTierShared
	winner.IntendedModel = "claude-sonnet-4-6"
	telemetryRow := turnrecord.InsertTelemetryParams{RequestedModel: "auto", DecisionModel: "gpt-5.4"}
	applySubscriberTelemetry(ctx, &telemetryRow)
	assert.Equal(t, "claude-sonnet", telemetryRow.IntendedModelFamily)
	assert.Equal(t, "gpt", telemetryRow.FinalModelFamily)
	assert.Equal(t, "00000000-0000-0000-0000-000000000101", telemetryRow.SubscriptionAccountID)
	assert.Equal(t, "00000000-0000-0000-0000-000000000102", telemetryRow.SubscriptionOwnerID)
	assert.Equal(t, auth.SubscriptionTierShared, telemetryRow.SubscriptionTier)

	attrs := telemetry.NewAttrBuilder(6)
	(&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}).applySubscriptionSpanTelemetry(ctx, attrs, "auto", "gpt-5.4")
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
	ctx := credentials.WithManagedSubscriptionUsage(context.Background())
	usage := mustAs[*credentials.ManagedSubscriptionUsage](t, ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}))
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
	assert.False(t, (&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}).costNeutralSubscriptionServed(ctx))
	telemetry := turnrecord.InsertTelemetryParams{RequestedModel: "claude-sonnet-5", DecisionModel: "claude-sonnet-5"}
	applySubscriberTelemetry(ctx, &telemetry)
	assert.Empty(t, telemetry.SubscriptionAccountID)
	assert.Empty(t, telemetry.SubscriptionOwnerID)
	assert.Empty(t, telemetry.SubscriptionTier)
}

func TestSubscriptionWinnerTelemetryDoesNotAttributeUnservedOrPaidCapacity(t *testing.T) {
	for _, served := range []bool{false, true} {
		ctx := credentials.WithManagedSubscriptionUsage(context.Background())
		winner := mustAs[*credentials.ManagedSubscriptionUsage](t, ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}))
		winner.Served = served
		winner.OverageInUse = served
		winner.SubscriptionAccountID = "00000000-0000-0000-0000-000000000101"
		winner.SubscriptionOwnerID = "00000000-0000-0000-0000-000000000102"
		winner.SubscriptionTier = auth.SubscriptionTierShared
		telemetry := turnrecord.InsertTelemetryParams{RequestedModel: "unknown", DecisionModel: "unknown"}
		applySubscriberTelemetry(ctx, &telemetry)
		assert.Empty(t, telemetry.SubscriptionAccountID)
		assert.Empty(t, telemetry.SubscriptionOwnerID)
		assert.Empty(t, telemetry.SubscriptionTier)
		assert.Empty(t, telemetry.IntendedModelFamily)
		assert.Empty(t, telemetry.FinalModelFamily)
	}
}
