package proxy

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"
)

const boostSourceOptimizerVersion = "boost-v1"

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
	if telemetry.SubscriberPlan == string(entitlement.PlanBoost) {
		telemetry.BoostOptimizerVersion = boostSourceOptimizerVersion
	}
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
