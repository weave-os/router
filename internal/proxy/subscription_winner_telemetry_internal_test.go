package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnterpriseSeatTelemetryUsesTheServingCredential(t *testing.T) {
	enterprise := &requestcontext.Credentials{APIKey: []byte("synthetic-enterprise"), OAuth: true, Source: requestcontext.SourceSubscription, SubscriptionPlan: providers.SubscriptionPlanEnterprise}
	team := &requestcontext.Credentials{APIKey: []byte("synthetic-team"), OAuth: true, Source: requestcontext.SourceSubscription, SubscriptionPlan: providers.SubscriptionPlanTeam}
	unknown := &requestcontext.Credentials{APIKey: []byte("synthetic-unknown"), OAuth: true, Source: requestcontext.SourceSubscription}
	codex := &requestcontext.Credentials{APIKey: []byte("synthetic-codex"), OAuth: true, Source: requestcontext.SourceCodexSubscription}
	apiKey := &requestcontext.Credentials{APIKey: []byte("synthetic-key"), Source: requestcontext.SourceBYOK}

	for _, tc := range []struct {
		name    string
		winner  *requestcontext.Credentials
		overage bool
		source  string
	}{
		{name: "included Enterprise", winner: enterprise, source: requestcontext.SourceSubscription},
		{name: "Enterprise overage", winner: enterprise, overage: true, source: requestcontext.SourceSubscriptionOverage},
		{name: "Team fallback", winner: team, source: requestcontext.SourceSubscription},
		{name: "Unverified fallback", winner: unknown, source: requestcontext.SourceSubscription},
		{name: "Codex fallback", winner: codex, source: requestcontext.SourceCodexSubscription},
		{name: "API fallback", winner: apiKey, source: requestcontext.SourceBYOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(credentials.WithManagedSubscriptionUsage(context.Background()), requestcontext.CredentialsContextKey{}, enterprise)
			ctx = context.WithValue(ctx, claudeSubscriptionPlanKey{}, providers.SubscriptionPlanEnterprise)
			winnerCtx := context.WithValue(ctx, requestcontext.CredentialsContextKey{}, tc.winner)
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
	ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("oauth-sentinel"),
		OAuth:  true,
		Source: requestcontext.SourceSubscription,
	})
	apiCtx := context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &requestcontext.Credentials{
		APIKey: []byte("api-sentinel"),
		Source: requestcontext.SourceBYOK,
	})

	recordWinningCredentials(ctx, apiCtx)

	require.Same(t, credentials.CredentialsFromContext(apiCtx), credentials.CredentialsFromContext(ctx))
	winner := credentials.CredentialsFromContext(ctx)
	require.Equal(t, requestcontext.SourceBYOK, winner.Source)
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
