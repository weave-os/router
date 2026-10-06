package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"
)

// applyServingTelemetry stamps the managed serving identity on every admitted turn.
func applyServingTelemetry(ctx context.Context, telemetry *InsertTelemetryParams) {
	identity, ok := requestcontext.ServingIdentityFromContext(ctx)
	if !ok {
		return
	}
	telemetry.ServingTarget = identity.Target
	telemetry.ServingProfileID = identity.ProfileKey
	telemetry.ServingProfileVersion = identity.ProfileRevision
	telemetry.ServingReleaseID = identity.ReleaseID
	telemetry.ServingBindingID = identity.BindingID
}

func applySubscriberTelemetry(ctx context.Context, telemetry *InsertTelemetryParams) {
	applyServingTelemetry(ctx, telemetry)
	applySubscriptionWinnerTelemetry(ctx, telemetry)
	plan, hasPlan := entitlement.ProductScopeFromContext(ctx)
	coverage, hasCoverage := entitlement.CoverageFromContext(ctx)
	_, hasPrepaidAuthorization := billing.PrepaidAuthorizationFromContext(ctx)
	if !hasPlan && !hasCoverage && !hasPrepaidAuthorization && !billing.HasOverrideFromContext(ctx) {
		return
	}
	if hasPlan {
		telemetry.SubscriberPlan = string(plan)
	}
	if hasCoverage {
		if telemetry.SubscriberPlan == "" {
			telemetry.SubscriberPlan = string(coverage.Plan)
		}
		version := coverage.EntitlementVersion
		telemetry.EntitlementVersion = &version
	}
	source := telemetryCapacitySource(ctx, telemetry.CredentialSource)
	if source == "" || !subscriberUsageObserved(telemetry) {
		return
	}
	telemetry.CapacitySource = string(source)
	retail := catalog.USDToMicros(telemetry.ActualInputCostUSD + telemetry.ActualOutputCostUSD)
	telemetry.RetailUsageMicros = &retail
	switch source {
	case entitlement.CapacitySourceIncludedRouter:
		telemetry.IncludedUsageMicros = &retail
	case entitlement.CapacitySourceLinkedClaude, entitlement.CapacitySourceLinkedCodex:
		telemetry.LinkedUsageMicros = &retail
	case entitlement.CapacitySourcePrepaid:
		telemetry.PrepaidUsageMicros = &retail
	}
}

type subscriberSettlementState struct {
	includedFailed bool
	prepaidFailed  bool
}

func captureSubscriberSettlementState(ctx context.Context) subscriberSettlementState {
	return subscriberSettlementState{
		includedFailed: entitlement.SettlementFailed(ctx),
		prepaidFailed:  billing.PrepaidSettlementFailed(ctx),
	}
}

// markSubscriberTelemetryUnsettled flags subscriber usage on a turn that
// failed before settlement ran, so reconciliation does not read its usage
// columns as settled spend.
func markSubscriberTelemetryUnsettled(telemetry *InsertTelemetryParams) {
	switch entitlement.CapacitySource(telemetry.CapacitySource) {
	case entitlement.CapacitySourceIncludedRouter, entitlement.CapacitySourcePrepaid:
		failed := true
		telemetry.SettlementFailed = &failed
	}
}

func applySubscriberSettlementTelemetry(state subscriberSettlementState, telemetry *InsertTelemetryParams) {
	switch entitlement.CapacitySource(telemetry.CapacitySource) {
	case entitlement.CapacitySourceIncludedRouter:
		failed := state.includedFailed
		telemetry.SettlementFailed = &failed
	case entitlement.CapacitySourcePrepaid:
		failed := state.prepaidFailed
		telemetry.SettlementFailed = &failed
	}
}

func telemetryCapacitySource(ctx context.Context, credentialSource string) entitlement.CapacitySource {
	if billing.HasOverrideFromContext(ctx) {
		return entitlement.CapacitySourceBillingOverride
	}
	switch credentialSource {
	case credSourceSubscription, credSourceSubscriptionOverage:
		return entitlement.CapacitySourceLinkedClaude
	case credSourceCodexSubscription:
		return entitlement.CapacitySourceLinkedCodex
	case credSourceBYOK:
		return ""
	}
	if _, ok := billing.PrepaidAuthorizationFromContext(ctx); ok {
		return entitlement.CapacitySourcePrepaid
	}
	if _, ok := entitlement.CoverageFromContext(ctx); ok {
		return entitlement.CapacitySourceIncludedRouter
	}
	return ""
}

func subscriberUsageObserved(telemetry *InsertTelemetryParams) bool {
	return telemetry.InputTokens != 0 ||
		telemetry.OutputTokens != 0 ||
		telemetry.CacheCreationTokens != nil ||
		telemetry.CacheReadTokens != nil
}

func canonicalTelemetryModelFamily(model string) string {
	canonical, known := catalog.ByID(model)
	if !known {
		return ""
	}
	family, _, versioned := catalog.FamilyAndVersion(canonical.ID)
	if versioned {
		return family
	}
	return canonical.ID
}

func applySubscriptionWinnerTelemetry(ctx context.Context, telemetry *InsertTelemetryParams) {
	telemetry.FinalModelFamily = canonicalTelemetryModelFamily(telemetry.DecisionModel)
	telemetry.IntendedModelFamily = canonicalTelemetryModelFamily(telemetry.RequestedModel)
	winningUsage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if winningUsage == nil {
		return
	}
	if winningUsage.IntendedModel != "" {
		telemetry.IntendedModelFamily = canonicalTelemetryModelFamily(winningUsage.IntendedModel)
	}
	if !winningUsage.Served || winningUsage.OverageInUse {
		return
	}
	telemetry.SubscriptionAccountID = winningUsage.SubscriptionAccountID
	telemetry.SubscriptionOwnerID = winningUsage.SubscriptionOwnerID
	telemetry.SubscriptionTier = winningUsage.SubscriptionTier
}

func (s *Service) applySubscriptionSpanTelemetry(ctx context.Context, attrs *otel.AttrBuilder, requestedModel, selectedModel string) {
	if keyID := apiKeyIDFromContext(ctx); keyID != "" {
		attrs.String("api_key_id", keyID)
	}
	_, _, source := s.credentialKeyParts(ctx)
	if source != "" {
		attrs.String("credential.source", source)
	}
	telemetry := InsertTelemetryParams{RequestedModel: requestedModel, DecisionModel: selectedModel}
	applySubscriptionWinnerTelemetry(ctx, &telemetry)
	if telemetry.SubscriptionAccountID != "" {
		attrs.String("subscription.account_id", telemetry.SubscriptionAccountID).
			String("subscription.owner_id", telemetry.SubscriptionOwnerID).
			String("subscription.tier", string(telemetry.SubscriptionTier))
	}
	if telemetry.IntendedModelFamily != "" {
		attrs.String("model.intended_family", telemetry.IntendedModelFamily)
	}
	if telemetry.FinalModelFamily != "" {
		attrs.String("model.final_family", telemetry.FinalModelFamily)
	}
}

// Decision attribution keeps admitted failures in their original family even
// when credential selection rejects the request before an upstream span starts.
func applySubscriptionDecisionSpanTelemetry(ctx context.Context, attrs *otel.AttrBuilder, requestedModel, selectedModel string) {
	if keyID := apiKeyIDFromContext(ctx); keyID != "" {
		attrs.String("api_key_id", keyID)
	}
	intendedModel := requestedModel
	if canonicalTelemetryModelFamily(intendedModel) == "" {
		intendedModel = selectedModel
	}
	if family := canonicalTelemetryModelFamily(intendedModel); family != "" {
		attrs.String("model.intended_family", family)
	}
}
