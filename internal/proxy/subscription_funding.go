package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
)

func subscriptionPoolForModel(target inference.Target) (subscriptions.Provider, bool) {
	if entry, known := catalog.ByID(target.CatalogID); known {
		for _, binding := range entry.Providers {
			if pool, compatible := managedSubscriptionProviderFromUpstream(binding.Provider, target.CatalogID); compatible {
				return pool, true
			}
		}
	}
	return managedSubscriptionProviderFromUpstream(target.Provider, target.CatalogID)
}

func (s *Service) withSubscriptionFundingAuthorization(ctx context.Context, headers http.Header) context.Context {
	ctx = credentials.WithManagedSubscriptionUsage(ctx)
	ctx, _ = withSubscriptionAdmissionBudget(ctx)
	if dispatch.AuthorizationFromContext(ctx) != nil {
		return ctx
	}
	codexToken, claudeToken := presentSubscriptionTokens(ctx, headers)
	if creds := CredentialsFromContext(ctx); creds != nil && creds.OAuth {
		if creds.Source == requestcontext.SourceCodexSubscription {
			codexToken = string(creds.APIKey)
		} else {
			claudeToken = string(creds.APIKey)
		}
	}
	ctx = s.withClaudeSubscriptionProfile(ctx, headers)
	admissionCtx := ctx
	return dispatch.WithAuthorization(ctx, func(callCtx context.Context, target inference.Target) error {
		if weaveCreditsDenied(callCtx) && !customerFunded(CredentialsFromContext(callCtx)) {
			return ErrWeaveCreditsDisabled
		}
		if claudeEnterpriseTarget(callCtx, target) || claudeEnterpriseTurn(callCtx) {
			return claudeEnterpriseFunding(callCtx, target, s.supportsSubscriptionTransport(target.Provider))
		}
		if quotaProofExempt(callCtx, CredentialsFromContext(callCtx)) {
			return nil
		}
		if byokFirst(callCtx, target.Provider) {
			return nil
		}
		pool, compatible := subscriptionPoolForModel(target)
		claudeTurn, _ := callCtx.Value(claudeSubscriptionTurnKey{}).(bool)
		presentedPlan, _ := callCtx.Value(claudeSubscriptionPlanKey{}).(providers.SubscriptionPlan)
		unverified := claudeSubscriptionPlan(callCtx) == providers.SubscriptionPlanUnknown && (claudeTurn || compatible && pool == subscriptions.ProviderClaude) || claudeTurn && presentedPlan == providers.SubscriptionPlanUnknown
		if state, _ := callCtx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); state != nil {
			unverified = unverified || state.ClaudePlanUnverified
		}
		if claudeEnterpriseEnabled(callCtx) && unverified && !triedSubscriptionFallsBackToCredits(callCtx) {
			observability.FromContext(callCtx).Warn("Blocked paid recovery while Claude subscription identity is unverified", "provider", target.Provider, "model", target.CatalogID)
			return fmt.Errorf("%w: Claude subscription plan could not be verified before paid fallback", credentials.ErrSubscriptionPoolUnavailable)
		}
		if !compatible {
			return nil
		}
		if pickedModelOnOwnPlan(callCtx) {
			return credentials.SubscriptionPoolUnavailableBecause(callCtx, "picked model runs on the caller's own plan")
		}
		refusal := clientAppRefusalFunding{counts: clientAppRefusalPaidFallback(callCtx), poolRefused: claudePoolRefusesClientApp(callCtx), clientApp: ClientIdentityFrom(callCtx).ClientApp}
		if err := s.authorizeSubscriptionAPI(admissionCtx, pool, target, codexToken, claudeToken, refusal); err != nil {
			observability.FromContext(callCtx).Warn("Blocked API inference without subscription quota-exhaustion evidence", "model", target.CatalogID, "provider", target.Provider, "subscription_provider", pool, "err", err)
			return err
		}
		return nil
	})
}

func (s *Service) subscriptionAPIAuthorization(ctx context.Context, provider, model string) error {
	ctx = s.withSubscriptionFundingAuthorization(ctx, nil)
	return dispatch.AuthorizationFromContext(ctx)(withPaidFallbackCredentials(ctx, provider), inference.Target{Provider: provider, CatalogID: model})
}

type clientAppRefusalFunding struct {
	counts      bool
	poolRefused bool
	clientApp   string
}

func (s *Service) authorizeSubscriptionAPI(ctx context.Context, pool subscriptions.Provider, target inference.Target, codexToken, claudeToken string, refusal clientAppRefusalFunding) error {
	if managedSubscriptionEnrollmentUnavailable(ctx) {
		return credentials.SubscriptionPoolUnavailableBecause(ctx, "subscription enrollment could not be verified")
	}
	now := s.clock.Now()
	if includedLaneCannotServe(ctx, pool, target, now) {
		return nil
	}
	candidates, snapshotPresent := ctx.Value(credentials.ManagedSubscriptionCandidatesContextKey{}).([]credentials.ManagedSubscriptionCandidate)
	throttledBlocker := false
	for _, candidate := range candidates {
		if candidate.Provider != auth.SubscriptionProvider(pool) || !candidate.Enabled || candidate.State == auth.SubscriptionAccountStateDisabled || candidate.State == auth.SubscriptionAccountStateReconnectRequired {
			continue
		}
		if s.managedAccountQuotaExhausted(ctx, candidate, now) || (candidate.State == auth.SubscriptionAccountStateCooldown && candidate.ResetAt.After(now) && candidate.CredentialRejected) {
			continue
		}
		if s.credentials.SubscriptionModels.ManagedDenied("account:"+candidate.AccountID, candidate.AccountID, target.Provider, target.CatalogID, now) {
			continue
		}
		if refusal.counts && pool == subscriptions.ProviderClaude && (refusal.poolRefused || s.credentials.SubscriptionModels.ManagedClientAppDenied(candidate.AccountID, providers.ProviderAnthropic, refusal.clientApp, now)) {
			continue
		}
		if credentials.ManagedAccountThrottled(ctx, candidate, now) {
			throttledBlocker = true
			continue
		}
		return credentials.SubscriptionPoolUnavailableBecause(ctx, "included quota is available or unknown")
	}
	token := claudeToken
	if pool == subscriptions.ProviderCodex {
		token = codexToken
	}
	callerQuotaUnproven := token != "" && !s.credentials.CallerQuotaSpent(ctx, pool, token, now)
	if throttledBlocker {
		if err := callerPlanRefusal(ctx, pool, callerQuotaUnproven, now); err != nil {
			return err
		}
		return throttledPoolRefusal(ctx, pool, token != "", now)
	}
	if err := credentials.MissingQuotaSnapshotRefusal(ctx, pool, snapshotPresent); err != nil {
		return err
	}
	return callerPlanRefusal(ctx, pool, callerQuotaUnproven, now)
}

func callerPlanRefusal(ctx context.Context, pool subscriptions.Provider, callerQuotaUnproven bool, now time.Time) error {
	if !callerQuotaUnproven {
		return nil
	}
	if callerPlanThrottled(ctx, pool, now) {
		if !billing.WeaveCreditsDeniedFromContext(ctx) {
			return nil
		}
		return credentials.SubscriptionPoolRefusal(ctx, subscriptionlane.CallerPlanThrottled)
	}
	if triedSubscriptionFallsBackToCredits(ctx) {
		return nil
	}
	return credentials.SubscriptionPoolUnavailableBecause(ctx, "caller subscription quota is available or unknown")
}

func triedSubscriptionFallsBackToCredits(ctx context.Context) bool {
	state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	if state == nil || !state.SubscriptionAttempted || state.CallerCredentialRejected || billing.WeaveCreditsDeniedFromContext(ctx) {
		return false
	}
	passthrough, _ := callerRoutingState(ctx)
	return !passthrough
}

func recordSubscriptionUnavailable(ctx context.Context, provider, model string, attemptErr error, now time.Time) {
	pool, compatible := managedSubscriptionProviderFromUpstream(provider, model)
	if !compatible || !upstreamServerFailure(attemptErr) {
		return
	}
	markSubscriptionPoolUnavailable(ctx, pool, now)
}

func markSubscriptionPoolUnavailable(ctx context.Context, pool subscriptions.Provider, now time.Time) {
	if callerModelPassthroughActive(ctx) {
		return
	}
	credentials.MarkSubscriptionPoolUnavailable(ctx, pool, now)
}

func upstreamServerFailure(err error) bool {
	var verificationUnavailable *providers.QuotaVerificationUnavailableError
	if errors.As(err, &verificationUnavailable) {
		return false
	}
	var buffered *providers.UpstreamErrorResponse
	if !errors.As(err, &buffered) || buffered.Status < http.StatusInternalServerError {
		return false
	}
	if errors.Is(err, providers.ErrUpstreamEmptyCompletion) {
		return false
	}
	if _, quota := codexQuotaExhaustion(err); quota {
		return false
	}
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(buffered.Body, &envelope) == nil {
		for _, label := range []string{envelope.Error.Type, envelope.Error.Code} {
			if _, quota := codexQuotaErrorTypes[label]; quota || strings.Contains(label, "rate_limit") {
				return false
			}
		}
	}
	return true
}

func (s *Service) subscriptionPoolUnavailable(ctx context.Context, provider, model string) bool {
	pool, compatible := managedSubscriptionProviderFromUpstream(provider, model)
	return compatible && credentials.SubscriptionProviderUnavailable(ctx, pool, s.clock.Now())
}

func (s *Service) linkedSeatCanCover(ctx context.Context, provider, model string) bool {
	if s.credentials.ManagedSubscriptions == nil || !managedSubscriptionCanServe(ctx, provider, model) || managedSubscriptionEnrollmentUnavailable(ctx) {
		return false
	}
	pool, _ := managedSubscriptionProviderFromUpstream(provider, model)
	now := s.clock.Now()
	return managedSeatCanCover(ctx, pool, now, func(candidate credentials.ManagedSubscriptionCandidate) bool {
		return s.managedAccountQuotaExhausted(ctx, candidate, now)
	})
}

type seatAdmission struct {
	now               time.Time
	observer          *usage.Observer
	codexExtraCredits bool
	verifiedPlanOnly  bool
}

func seatAdmissionOf(s *Service) seatAdmission {
	return seatAdmission{now: s.clock.Now(), observer: s.credentials.UsageObserver, codexExtraCredits: s.codexExtraCreditsAllowed()}
}

func (a seatAdmission) mayAdmit(ctx context.Context, provider, model string) bool {
	if !managedSubscriptionCanServe(ctx, provider, model) || managedSubscriptionEnrollmentUnavailable(ctx) {
		return false
	}
	pool, _ := managedSubscriptionProviderFromUpstream(provider, model)
	enterpriseOverage := pool == subscriptions.ProviderClaude && (claudeEnterpriseOnly(ctx) || !a.verifiedPlanOnly && claudeEnterpriseEnabled(ctx))
	return managedSeatCanCover(ctx, pool, a.now, func(candidate credentials.ManagedSubscriptionCandidate) bool {
		switch {
		case enterpriseOverage:
			return false
		case recordedQuotaExhausted(ctx, candidate, a.now):
			return true
		case pool == subscriptions.ProviderCodex && a.codexExtraCredits:
			return false
		default:
			return includedQuotaObservedSpent(a.observer, candidate, a.now)
		}
	})
}

func managedSeatCanCover(ctx context.Context, pool subscriptions.Provider, now time.Time, quotaExhausted func(credentials.ManagedSubscriptionCandidate) bool) bool {
	if credentials.SubscriptionProviderUnavailable(ctx, pool, now) {
		return false
	}
	candidates, snapshotPresent := ctx.Value(credentials.ManagedSubscriptionCandidatesContextKey{}).([]credentials.ManagedSubscriptionCandidate)
	if !snapshotPresent {
		return true
	}
	for _, candidate := range candidates {
		if candidate.Provider != auth.SubscriptionProvider(pool) || !candidate.Enabled || candidate.State == auth.SubscriptionAccountStateDisabled || candidate.State == auth.SubscriptionAccountStateReconnectRequired {
			continue
		}
		if candidate.State == auth.SubscriptionAccountStateCooldown && candidate.ResetAt.After(now) {
			continue
		}
		if !quotaExhausted(candidate) {
			return true
		}
	}
	return false
}

func (s *Service) managedAccountQuotaExhausted(ctx context.Context, candidate credentials.ManagedSubscriptionCandidate, now time.Time) bool {
	if recordedQuotaExhausted(ctx, candidate, now) {
		return true
	}
	if candidate.Provider == auth.SubscriptionProviderClaude && claudeEnterpriseOnly(ctx) {
		return false
	}
	return includedQuotaObservedSpent(s.credentials.UsageObserver, candidate, now)
}

func includedQuotaObservedSpent(observer *usage.Observer, candidate credentials.ManagedSubscriptionCandidate, now time.Time) bool {
	if observer == nil {
		return false
	}
	snapshot, observed := observer.Snapshot(observer.Key([]byte("subscription-account:" + candidate.AccountID)))
	return observed && credentials.IncludedQuotaSpent(snapshot, now)
}

func recordedQuotaExhausted(ctx context.Context, candidate credentials.ManagedSubscriptionCandidate, now time.Time) bool {
	if state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); state != nil {
		if state.QuotaExhaustedAccounts[candidate.AccountID].After(now) {
			return true
		}
	}
	if candidate.Provider == auth.SubscriptionProviderClaude && claudeEnterpriseOnly(ctx) {
		return false
	}
	return candidate.State == auth.SubscriptionAccountStateExhausted && candidate.ResetAt.After(now)
}

func credentialRejectionStands(subscriptionErr, retryErr error, credentialRejected func(error) bool) bool {
	return errors.Is(retryErr, dispatch.ErrAuthorizationDenied) && credentialRejected(subscriptionErr)
}

func quotaProofExempt(ctx context.Context, creds *Credentials) bool {
	return creds != nil && creds.OAuth || paidCapacityUnfunded(ctx) && unfundedKeyFunds(ctx, creds)
}
