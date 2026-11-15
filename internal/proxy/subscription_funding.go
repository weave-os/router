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
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

type managedSubscriptionCandidatesKey struct{}

type managedSubscriptionCandidate struct {
	accountID          string
	provider           auth.SubscriptionProvider
	state              auth.SubscriptionAccountState
	resetAt            time.Time
	enabled            bool
	credentialRejected bool
}

func WithManagedSubscriptionCandidates(ctx context.Context, accounts []*auth.SubscriptionAccount) context.Context {
	candidates := make([]managedSubscriptionCandidate, 0, len(accounts))
	enrolled := make(map[auth.SubscriptionProvider]struct{})
	for _, account := range accounts {
		candidate := managedSubscriptionCandidate{accountID: account.ID, provider: account.Provider, state: account.State, enabled: account.Enabled, credentialRejected: account.CredentialRejections > 0}
		if account.CooldownUntil != nil {
			candidate.resetAt = *account.CooldownUntil
		}
		candidates = append(candidates, candidate)
		enrolled[account.Provider] = struct{}{}
	}
	ctx = context.WithValue(ctx, managedSubscriptionCandidatesKey{}, candidates)
	return context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, enrolled)
}

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
	ctx = WithManagedSubscriptionUsage(ctx)
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
		if creds := CredentialsFromContext(callCtx); creds != nil && creds.OAuth {
			return nil
		}
		if byokFirst(callCtx, target.Provider) {
			return nil
		}
		pool, compatible := subscriptionPoolForModel(target)
		claudeTurn, _ := callCtx.Value(claudeSubscriptionTurnKey{}).(bool)
		presentedPlan, _ := callCtx.Value(claudeSubscriptionPlanKey{}).(providers.SubscriptionPlan)
		unverified := claudeSubscriptionPlan(callCtx) == providers.SubscriptionPlanUnknown && (claudeTurn || compatible && pool == subscriptions.ProviderClaude) || claudeTurn && presentedPlan == providers.SubscriptionPlanUnknown
		if state, _ := callCtx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
			unverified = unverified || state.ClaudePlanUnverified
		}
		if claudeEnterpriseEnabled(callCtx) && unverified {
			observability.FromContext(callCtx).Warn("Blocked paid recovery while Claude subscription identity is unverified", "provider", target.Provider, "model", target.CatalogID)
			return fmt.Errorf("%w: Claude subscription plan could not be verified before paid fallback", ErrSubscriptionPoolUnavailable)
		}
		if !compatible {
			return nil
		}
		if pickedModelOnOwnPlan(callCtx) {
			return fmt.Errorf("%w: picked model runs on the caller's own plan", ErrSubscriptionPoolUnavailable)
		}
		refusal := clientAppRefusalFunding{counts: clientAppRefusalPaidFallback(callCtx), poolRefused: claudePoolRefusesClientApp(callCtx), clientApp: ClientIdentityFrom(callCtx).ClientApp}
		if err := s.authorizeSubscriptionAPI(admissionCtx, pool, codexToken, claudeToken, refusal); err != nil {
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

func (s *Service) authorizeSubscriptionAPI(ctx context.Context, pool subscriptions.Provider, codexToken, claudeToken string, refusal clientAppRefusalFunding) error {
	if managedSubscriptionEnrollmentUnavailable(ctx) {
		return fmt.Errorf("%w: subscription enrollment could not be verified", ErrSubscriptionPoolUnavailable)
	}
	now := s.clockNow()
	if subscriptionProviderUnavailable(ctx, pool, now) {
		return nil
	}
	candidates, snapshotPresent := ctx.Value(managedSubscriptionCandidatesKey{}).([]managedSubscriptionCandidate)
	throttledBlocker := false
	for _, candidate := range candidates {
		if candidate.provider != auth.SubscriptionProvider(pool) || !candidate.enabled || candidate.state == auth.SubscriptionAccountStateDisabled || candidate.state == auth.SubscriptionAccountStateReconnectRequired {
			continue
		}
		if s.managedAccountQuotaExhausted(ctx, candidate, now) || managedAccountCredentialCooling(candidate, now) {
			continue
		}
		if refusal.counts && pool == subscriptions.ProviderClaude && (refusal.poolRefused || s.subscriptionModels.managedClientAppDenied(candidate.accountID, providers.ProviderAnthropic, refusal.clientApp, now)) {
			continue
		}
		if managedAccountThrottled(ctx, candidate, now) {
			throttledBlocker = true
			continue
		}
		return fmt.Errorf("%w: included quota is available or unknown", ErrSubscriptionPoolUnavailable)
	}
	token := claudeToken
	if pool == subscriptions.ProviderCodex {
		token = codexToken
	}
	callerQuotaUnproven := token != "" && !s.callerQuotaSpent(ctx, pool, token, now)
	if throttledBlocker {
		if err := callerPlanRefusal(ctx, pool, callerQuotaUnproven, now); err != nil {
			return err
		}
		return ErrSubscriptionPoolThrottled
	}
	if !snapshotPresent {
		enrolled, _ := ctx.Value(ManagedSubscriptionProvidersContextKey{}).(map[auth.SubscriptionProvider]struct{})
		if _, present := enrolled[auth.SubscriptionProvider(pool)]; present {
			return fmt.Errorf("%w: enrolled pool has no quota snapshot", ErrSubscriptionPoolUnavailable)
		}
	}
	return callerPlanRefusal(ctx, pool, callerQuotaUnproven, now)
}

func callerPlanRefusal(ctx context.Context, pool subscriptions.Provider, callerQuotaUnproven bool, now time.Time) error {
	if !callerQuotaUnproven {
		return nil
	}
	if callerPlanThrottled(ctx, pool, now) {
		return errCallerPlanThrottled
	}
	return fmt.Errorf("%w: caller subscription quota is available or unknown", ErrSubscriptionPoolUnavailable)
}

func (s *Service) callerQuotaSpent(ctx context.Context, pool subscriptions.Provider, token string, now time.Time) bool {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil && state.QuotaExhaustedProviders[pool].After(now) {
		return true
	}
	if s.usageObserver != nil {
		if snapshot, observed := s.usageObserver.Snapshot(s.usageObserver.Key([]byte(token))); observed && includedQuotaSpent(snapshot, now) {
			return true
		}
	}
	return false
}

const subscriptionUnavailableCooldown = time.Minute

func (s *Service) recordSubscriptionUnavailable(ctx context.Context, provider, model string, attemptErr error) {
	pool, compatible := managedSubscriptionProviderFromUpstream(provider, model)
	if !compatible || !upstreamServerFailure(attemptErr) {
		return
	}
	s.markSubscriptionPoolUnavailable(ctx, pool)
}

func (s *Service) markSubscriptionPoolUnavailable(ctx context.Context, pool subscriptions.Provider) {
	if callerModelPassthroughActive(ctx) {
		return
	}
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if state == nil {
		return
	}
	if state.UnavailableProviders == nil {
		state.UnavailableProviders = make(map[subscriptions.Provider]time.Time)
	}
	state.UnavailableProviders[pool] = s.clockNow().Add(subscriptionUnavailableCooldown)
	observability.FromContext(ctx).Warn("Subscription inference failed on provider capacity; paid rescue eligible subject to funding authorization")
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
	return compatible && subscriptionProviderUnavailable(ctx, pool, s.clockNow())
}

func subscriptionProviderUnavailable(ctx context.Context, pool subscriptions.Provider, now time.Time) bool {
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return state != nil && state.UnavailableProviders[pool].After(now)
}

func (s *Service) linkedSeatCanCover(ctx context.Context, provider, model string) bool {
	if s.managedSubscriptions == nil || !managedSubscriptionCanServe(ctx, provider, model) || managedSubscriptionEnrollmentUnavailable(ctx) {
		return false
	}
	pool, _ := managedSubscriptionProviderFromUpstream(provider, model)
	now := s.clockNow()
	if subscriptionProviderUnavailable(ctx, pool, now) {
		return false
	}
	candidates, snapshotPresent := ctx.Value(managedSubscriptionCandidatesKey{}).([]managedSubscriptionCandidate)
	if !snapshotPresent {
		return true
	}
	for _, candidate := range candidates {
		if candidate.provider != auth.SubscriptionProvider(pool) || !candidate.enabled || candidate.state == auth.SubscriptionAccountStateDisabled || candidate.state == auth.SubscriptionAccountStateReconnectRequired {
			continue
		}
		if candidate.state == auth.SubscriptionAccountStateCooldown && candidate.resetAt.After(now) {
			continue
		}
		if !s.managedAccountQuotaExhausted(ctx, candidate, now) {
			return true
		}
	}
	return false
}

func managedAccountThrottled(ctx context.Context, candidate managedSubscriptionCandidate, now time.Time) bool {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		if state.ThrottledAccounts[candidate.accountID].After(now) {
			return true
		}
	}
	return candidate.state == auth.SubscriptionAccountStateCooldown && candidate.resetAt.After(now) && !candidate.credentialRejected
}

func managedAccountCredentialCooling(candidate managedSubscriptionCandidate, now time.Time) bool {
	return candidate.state == auth.SubscriptionAccountStateCooldown && candidate.resetAt.After(now) && candidate.credentialRejected
}

func (s *Service) managedAccountQuotaExhausted(ctx context.Context, candidate managedSubscriptionCandidate, now time.Time) bool {
	if state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); state != nil {
		if state.QuotaExhaustedAccounts[candidate.accountID].After(now) {
			return true
		}
	}
	if candidate.provider == auth.SubscriptionProviderClaude && claudeEnterpriseOnly(ctx) {
		return false
	}
	if candidate.state == auth.SubscriptionAccountStateExhausted && candidate.resetAt.After(now) {
		return true
	}
	if s.usageObserver != nil {
		snapshot, observed := s.usageObserver.Snapshot(s.usageObserver.Key([]byte("subscription-account:" + candidate.accountID)))
		return observed && includedQuotaSpent(snapshot, now)
	}
	return false
}

func includedQuotaSpent(snapshot usage.Snapshot, now time.Time) bool {
	return (snapshot.OverageInUse && snapshot.UnifiedResetAt.After(now)) || snapshot.ExhaustedAsOf(now)
}

func credentialRejectionStands(subscriptionErr, retryErr error, credentialRejected func(error) bool) bool {
	return errors.Is(retryErr, dispatch.ErrAuthorizationDenied) && credentialRejected(subscriptionErr)
}
