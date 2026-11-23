package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
)

func subscriptionOwnerFromContext(ctx context.Context) auth.SubscriptionOwner {
	if _, ok := requestcontext.InternalTestIdentityFrom(ctx); ok {
		return auth.SubscriptionOwner{}
	}
	if owner, ok := ctx.Value(credentials.SubscriptionOwnerContextKey{}).(auth.SubscriptionOwner); ok && owner.Valid() {
		return owner
	}
	return auth.SubscriptionOwner{APIKeyID: requestcontext.APIKeyIDFromContext(ctx)}
}

func managedSubscriptionEnrollmentUnavailable(ctx context.Context) bool {
	unavailable, _ := ctx.Value(credentials.ManagedSubscriptionEnrollmentUnavailableContextKey{}).(bool)
	return unavailable
}

func (s *Service) leaseManagedSubscription(ctx context.Context, provider, model string) (context.Context, subscriptions.Lease, bool, error) {
	if credentials.ByokFirst(ctx, provider) {
		return ctx, subscriptions.Lease{}, false, nil
	}
	poolProvider, eligible := credentials.ManagedSubscriptionProviderFromUpstream(provider, model)
	if eligible && managedSubscriptionEnrollmentUnavailable(ctx) {
		if creds := credentials.CredentialsFromContext(ctx); creds != nil && creds.OAuth && s.supportsSubscriptionTransport(provider) {
			return ctx, subscriptions.Lease{}, false, nil
		}
		return ctx, subscriptions.Lease{}, true, credentials.SubscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable)
	}
	if credentials.SubscriptionAPIOnly(ctx) {
		return ctx, subscriptions.Lease{}, false, nil
	}
	if credentials.SubscriptionAttemptOnly(ctx) && (!eligible || !s.supportsSubscriptionTransport(provider) || !credentials.ManagedSubscriptionEnrolled(ctx, poolProvider)) && !credentials.ServedOnSubscription(ctx) {
		return ctx, subscriptions.Lease{}, true, credentials.SubscriptionPoolExhausted(ctx)
	}
	if eligible && (credentials.ClaudeSubscriptionSuppressed(ctx) && provider == providers.ProviderAnthropic || credentials.CodexSubscriptionSuppressed(ctx) && provider == providers.ProviderOpenAI) && !credentials.ManagedSubscriptionEnrolled(ctx, poolProvider) && !s.managedProviderFallbackAvailable(ctx, poolProvider) {
		return ctx, subscriptions.Lease{}, true, credentials.SubscriptionPoolExhausted(ctx)
	}
	if !eligible || s.credentials.ManagedSubscriptions == nil || !credentials.ManagedSubscriptionEnrolled(ctx, poolProvider) ||
		poolProvider == subscriptions.ProviderCodex && credentials.CodexChatEndpoint(ctx) {
		return ctx, subscriptions.Lease{}, false, nil
	}
	if !s.supportsSubscriptionTransport(provider) {
		return ctx, subscriptions.Lease{}, false, nil
	}
	if callerLoginServesFirst(ctx, model, s.credentials.UsageObserver, &s.credentials.SubscriptionModels, s.clock.Now()) {
		return ctx, subscriptions.Lease{}, false, nil
	}

	diagnostics := subscriptionAdmissionDiagnostics{now: s.clock.Now, started: s.clock.Now()}
	defer diagnostics.log(ctx, poolProvider, model)

	owner := subscriptionOwnerFromContext(ctx)
	if winner, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); winner != nil {
		for pair := range winner.AttemptedAccounts {
			if strings.HasSuffix(pair, "\x00"+model) {
				owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, strings.TrimSuffix(pair, "\x00"+model))
			}
		}
	}
	seen := make(map[string]bool)
	var modelDenial error
	unadmitted := false
	poolSpent := func() (context.Context, subscriptions.Lease, bool, error) {
		if current := credentials.CredentialsFromContext(ctx); ctx.Err() == nil && current != nil && current.OAuth {
			return ctx, subscriptions.Lease{}, false, nil
		}
		if !unadmitted && !credentials.SubscriptionAttemptOnly(ctx) && !paidFallbackForbiddenFor(ctx, provider) && s.managedProviderFallbackAvailable(ctx, poolProvider) && s.subscriptionAPIAuthorization(ctx, provider, model) == nil {
			return ctx, subscriptions.Lease{}, false, nil
		}
		if unadmitted {
			return ctx, subscriptions.Lease{}, true, credentials.SubscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable)
		}
		if modelDenial != nil {
			return ctx, subscriptions.Lease{}, true, modelDenial
		}
		return ctx, subscriptions.Lease{}, true, linkedPoolExhausted(ctx, poolProvider, s.clock.Now())
	}
	for {
		if poolProvider == subscriptions.ProviderClaude && claudePoolRefusesClientApp(ctx) {
			return poolSpent()
		}
		leaseCtx := ctx
		if poolProvider == subscriptions.ProviderClaude && claudeEnterpriseEnabled(ctx) {
			leaseCtx = requestcontext.WithSubscriptionExtraUsage(ctx)
		}
		lease, present, err := leaseOrMarkLeasedOut(leaseCtx, s.credentials.ManagedSubscriptions, owner, poolProvider, model, requestcontext.ClientIdentityFrom(ctx).SessionID)
		if err != nil && !errors.Is(err, subscriptions.ErrNoAvailableAccount) {
			if current := credentials.CredentialsFromContext(ctx); ctx.Err() == nil && current != nil && current.OAuth {
				observability.FromContext(ctx).Warn("Subscription pool lease failed; using caller subscription", "provider", poolProvider, "err", err)
				return ctx, subscriptions.Lease{}, false, nil
			}
			if !unadmitted && !credentials.SubscriptionAttemptOnly(ctx) && !paidFallbackForbiddenFor(ctx, provider) && s.managedProviderFallbackAvailable(ctx, poolProvider) && s.subscriptionAPIAuthorization(ctx, provider, model) == nil {
				return ctx, subscriptions.Lease{}, false, nil
			}
			return ctx, subscriptions.Lease{}, present, errors.Join(credentials.SubscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolUnavailable), err)
		}
		if err != nil || !present {
			return poolSpent()
		}
		if seen[lease.AccountID] {
			diagnostics.skip(lease, subscriptionSkipRepeatedLease)
			lease.Release()
			return poolSpent()
		}
		seen[lease.AccountID] = true
		if candidates, snapshotPresent := ctx.Value(credentials.ManagedSubscriptionCandidatesContextKey{}).([]credentials.ManagedSubscriptionCandidate); snapshotPresent {
			admitted := false
			for _, candidate := range candidates {
				if candidate.AccountID == lease.AccountID && candidate.Provider == auth.SubscriptionProvider(poolProvider) {
					admitted = true
					break
				}
			}
			if !admitted {
				diagnostics.skip(lease, subscriptionSkipOutsideAdmission)
				lease.Release()
				observability.FromContext(ctx).Warn("Subscription lease is outside the request admission snapshot", "account_id", lease.AccountID, "provider", poolProvider)
				unadmitted = true
				owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
				continue
			}
		}
		if winner, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); winner != nil {
			if _, attempted := winner.AttemptedAccounts[lease.AccountID+"\x00"+model]; attempted {
				lease.Release()
				owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
				continue
			}
		}
		if poolProvider == subscriptions.ProviderClaude && s.credentials.SubscriptionModels.ManagedClientAppDenied(lease.AccountID, provider, requestcontext.ClientIdentityFrom(ctx).ClientApp, s.clock.Now()) {
			diagnostics.skip(lease, subscriptionSkipClientAppDenied)
			markSubscriptionClientAppRefused(ctx)
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		if s.credentials.SubscriptionModels.ManagedDenied(managedOverloadOwner(lease.AccountID), lease.AccountID, provider, model, s.clock.Now()) {
			diagnostics.skip(lease, subscriptionSkipOverloadCooldown)
			markSubscriptionPoolUnavailable(ctx, poolProvider, s.clock.Now())
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		if s.credentials.SubscriptionModels.ManagedDenied("account:"+lease.AccountID, lease.AccountID, provider, model, s.clock.Now()) {
			diagnostics.skip(lease, subscriptionSkipModelDenied)
			if poolProvider == subscriptions.ProviderCodex {
				modelDenial = codexSubscriptionModelUnavailable(model)
			} else {
				modelDenial = anthropicSubscriptionModelUnavailable(model)
			}
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		creds := &requestcontext.Credentials{APIKey: []byte(lease.AccessToken), OAuth: true, Source: requestcontext.SourceSubscription, PrincipalID: "subscription-account:" + lease.AccountID, SubscriptionAccountID: lease.AccountID}
		if poolProvider == subscriptions.ProviderCodex {
			creds.Source = requestcontext.SourceCodexSubscription
			creds.AccountID = []byte(lease.ProviderAccount)
			creds.PrincipalID = "chatgpt-account:" + lease.ProviderAccount
		}
		credentialCtx := context.WithValue(ctx, requestcontext.CredentialsContextKey{}, creds)
		if poolProvider == subscriptions.ProviderClaude {
			credentialCtx = s.withClaudeSubscriptionProfile(credentialCtx, nil)
			if state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage); state != nil && claudeSubscriptionPlan(credentialCtx) == providers.SubscriptionPlanUnknown {
				state.ClaudePlanUnverified = true
			}
		}
		snapshot, observed := s.managedSubscriptionUsageSnapshot(lease.AccountID, lease.AccessToken)
		allowClaudeOverage := poolProvider == subscriptions.ProviderClaude && claudeEnterpriseOnly(credentialCtx)
		if poolProvider == subscriptions.ProviderClaude && !lease.AllowsPlanAttempt(claudeSubscriptionPlan(credentialCtx), claudeEnterpriseEnabled(ctx), s.clock.Now()) {
			diagnostics.skip(lease, subscriptionSkipPlanIneligible)
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		if observed && snapshot.OverageInUse && !allowClaudeOverage {
			diagnostics.skip(lease, subscriptionSkipPaidOverage)
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		if observed && snapshot.ExhaustedAsOf(s.clock.Now()) && !allowClaudeOverage && (poolProvider != subscriptions.ProviderCodex || !s.codexExtraCreditsAllowed()) {
			diagnostics.skip(lease, subscriptionSkipQuotaExhausted)
			resetAt := linkedSubscriptionResetAt(snapshot, s.clock.Now())
			recordManagedQuotaProof(ctx, lease.AccountID, resetAt)
			if err := exhaustManagedSubscription(ctx, s.credentials.ManagedSubscriptions, owner, poolProvider, lease.AccountID, resetAt); err != nil {
				observability.FromContext(ctx).Error("Failed to persist exhausted subscription account", "provider", poolProvider, "account_id", lease.AccountID, "err", err)
			}
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		diagnostics.admitted = true
		return credentialCtx, lease, true, nil
	}
}

func (s *Service) managedSubscriptionUsageSnapshot(accountID, accessToken string) (usage.Snapshot, bool) {
	if s.credentials.UsageObserver == nil || accessToken == "" {
		return usage.Snapshot{}, false
	}
	if accountID != "" {
		if snapshot, found := s.credentials.UsageObserver.Snapshot(s.credentials.UsageObserver.Key([]byte("subscription-account:" + accountID))); found {
			return snapshot, true
		}
	}
	return s.credentials.UsageObserver.Snapshot(s.credentials.UsageObserver.Key([]byte(accessToken)))
}

func (s *Service) managedProviderFallbackAvailable(ctx context.Context, provider subscriptions.Provider) bool {
	switch provider {
	case subscriptions.ProviderClaude:
		return s.credentials.AnthropicFallbackKeyAvailable(ctx)
	case subscriptions.ProviderCodex:
		return s.openaiFallbackKeyAvailable(ctx)
	default:
		return false
	}
}

func exhaustManagedSubscription(ctx context.Context, leaser subscriptions.Leaser, owner auth.SubscriptionOwner, provider subscriptions.Provider, accountID string, resetAt time.Time) error {
	if health, ok := leaser.(interface {
		Exhaust(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string, time.Time) error
	}); ok {
		return health.Exhaust(ctx, owner, provider, accountID, resetAt)
	}
	return leaser.Cooldown(ctx, owner, provider, accountID, resetAt)
}

func (s *Service) recordManagedSubscriptionFailure(ctx context.Context, provider, model string, lease subscriptions.Lease, attemptErr error) bool {
	poolProvider, eligible := credentials.ManagedSubscriptionProviderFromUpstream(provider, model)
	if !eligible || lease.AccountID == "" {
		return false
	}
	status := upstreamStatus(attemptErr)
	owner := subscriptionOwnerFromContext(ctx)
	if provider == providers.ProviderAnthropic && anthropicSubscriptionModelRejected(attemptErr) {
		s.credentials.SubscriptionModels.DenyManaged("account:"+lease.AccountID, lease.AccountID, provider, model, s.clock.Now().Add(credentials.SubscriptionModelDenialTTL))
		observability.FromContext(ctx).Warn("Managed subscription account cannot access model",
			"provider", poolProvider, "account_id", lease.AccountID, "model", model)
		return true
	}
	if provider == providers.ProviderAnthropic && anthropicClientAppRefused(ctx, attemptErr) {
		clientApp := requestcontext.ClientIdentityFrom(ctx).ClientApp
		s.credentials.SubscriptionModels.DenyManagedClientApp(lease.AccountID, provider, clientApp, s.clock.Now().Add(subscriptionThirdPartyDenialTTL))
		markSubscriptionClientAppRefused(ctx)
		observability.FromContext(ctx).Warn("Managed subscription account refused third-party client on plan limits",
			"provider", poolProvider, "account_id", lease.AccountID, "client_app", clientApp)
		return true
	}
	if provider == providers.ProviderOpenAI && codexSubscriptionModelRejected(attemptErr) {
		s.credentials.SubscriptionModels.DenyManaged("account:"+lease.AccountID, lease.AccountID, provider, model, s.clock.Now().Add(credentials.SubscriptionModelDenialTTL))
		observability.FromContext(ctx).Warn("Managed subscription account cannot access model",
			"provider", poolProvider, "account_id", lease.AccountID, "model", model)
		return true
	}
	if provider == providers.ProviderAnthropic && providers.IsUpstreamCredentialRefusal(attemptErr) {
		return cooldownRefusedManagedSeat(ctx, s.credentials.ManagedSubscriptions, s.clock.Now(), owner, poolProvider, lease, attemptErr)
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden:
		return recordManagedSubscriptionStatus(ctx, s, owner, poolProvider, lease, attemptErr)
	default:
		if upstreamServerFailure(attemptErr) {
			s.credentials.SubscriptionModels.DenyManaged(managedOverloadOwner(lease.AccountID), lease.AccountID, provider, model, s.clock.Now().Add(managedOverloadCooldown))
			observability.FromContext(ctx).Warn("Managed subscription account overloaded; skipping it briefly",
				"provider", poolProvider, "account_id", lease.AccountID, "model", model, "upstream_status", status, "cooldown", managedOverloadCooldown)
		}
		return false
	}
}

func recordManagedSubscriptionStatus(ctx context.Context, s *Service, owner auth.SubscriptionOwner, poolProvider subscriptions.Provider, lease subscriptions.Lease, attemptErr error) bool {
	switch status := upstreamStatus(attemptErr); status {
	case http.StatusTooManyRequests:
		resetAt := managedSubscriptionResetAt(attemptErr, s.clock.Now())
		if quotaReset, exhausted := s.subscriptionQuotaExhaustion(poolProvider, lease, attemptErr); exhausted {
			resetAt = quotaReset
			recordManagedQuotaProof(ctx, lease.AccountID, resetAt)
			if poolProvider == subscriptions.ProviderClaude && claudeEnterpriseOnly(ctx) {
				if err := s.credentials.ManagedSubscriptions.Cooldown(ctx, owner, poolProvider, lease.AccountID, resetAt); err != nil {
					observability.FromContext(ctx).Error("Failed to persist Claude overage refusal cooldown", "account_id", lease.AccountID, "err", err)
				}
				return true
			}
			if err := exhaustManagedSubscription(ctx, s.credentials.ManagedSubscriptions, owner, poolProvider, lease.AccountID, resetAt); err != nil {
				observability.FromContext(ctx).Error("Failed to persist exhausted subscription account", "provider", poolProvider, "account_id", lease.AccountID, "err", err)
			}
			observability.FromContext(ctx).Warn("Subscription included quota exhausted", "provider", poolProvider, "account_id", lease.AccountID, "reset_at", resetAt)
			return true
		}
		if err := s.credentials.ManagedSubscriptions.Cooldown(ctx, owner, poolProvider, lease.AccountID, resetAt); err != nil {
			observability.FromContext(ctx).Error("Failed to persist subscription account cooldown",
				"provider", poolProvider, "account_id", lease.AccountID, "err", err)
		}
		recordManagedThrottle(ctx, lease.AccountID, resetAt)
		observability.FromContext(ctx).Warn("Subscription account throttled; quota exhaustion not established",
			"provider", poolProvider, "account_id", lease.AccountID, "cooldown_until", resetAt)
		return true
	case http.StatusUnauthorized, http.StatusForbidden:
		reconnect, err := rejectManagedSubscriptionCredential(ctx, s.credentials.ManagedSubscriptions, owner, poolProvider, lease.AccountID, status)
		if err != nil {
			observability.FromContext(ctx).Error("Failed to record rejected subscription account",
				"provider", poolProvider, "account_id", lease.AccountID, "err", err)
		}
		observability.FromContext(ctx).Warn("Subscription account credential rejected",
			"provider", poolProvider, "account_id", lease.AccountID, "upstream_status", status, "reconnect_required", reconnect)
		return true
	}
	return false
}

const managedOverloadCooldown = 30 * time.Second

func managedOverloadOwner(accountID string) string { return "overload:" + accountID }

func (s *Service) subscriptionQuotaExhaustion(pool subscriptions.Provider, lease subscriptions.Lease, attemptErr error) (time.Time, bool) {
	now := s.clock.Now()
	if pool == subscriptions.ProviderCodex {
		if resetAt, exhausted := codexQuotaExhaustion(attemptErr); exhausted {
			if resetAt.IsZero() {
				resetAt = now.Add(time.Duration(codexQuotaWindowMinutes) * time.Minute)
			}
			return resetAt, resetAt.After(now)
		}
	}
	var upstream *providers.UpstreamErrorResponse
	if errors.As(attemptErr, &upstream) {
		var snapshot usage.Snapshot
		var observed bool
		if pool == subscriptions.ProviderClaude {
			snapshot, observed = usage.ParseAnthropicUnifiedHeaders(upstream.Headers)
		} else {
			snapshot, observed = usage.ParseCodexHeaders(upstream.Headers)
		}
		snapshot.ObservedAt = now
		if observed && snapshot.ExhaustedAsOf(now) {
			return linkedSubscriptionResetAt(snapshot, now), true
		}
	}
	if snapshot, observed := s.managedSubscriptionUsageSnapshot(lease.AccountID, lease.AccessToken); observed && snapshot.ExhaustedAsOf(now) {
		return linkedSubscriptionResetAt(snapshot, now), true
	}
	return time.Time{}, false
}

func (s *Service) recordManagedSubscriptionSuccess(ctx context.Context, provider, model string, lease subscriptions.Lease) {
	if lease.AccountID == "" || lease.State == auth.SubscriptionAccountStateActive {
		return
	}
	poolProvider, eligible := credentials.ManagedSubscriptionProviderFromUpstream(provider, model)
	if !eligible {
		return
	}
	activateManagedSubscription(ctx, s.credentials.ManagedSubscriptions, subscriptionOwnerFromContext(ctx), poolProvider, lease)
}

func (s *Service) supportsSubscriptionTransport(provider string) bool {
	client, err := s.engine.Clients().Client(provider)
	if err != nil {
		return false
	}
	transport, ok := client.(providers.SubscriptionTransport)
	return ok && transport.SupportsSubscriptions()
}

func (s *Service) subscriptionAlternativeDecisions(ctx context.Context, req router.Request, selected router.Decision) []router.Decision {
	if credentials.SubscriptionRoutingDisabledForRequest(ctx) || credentials.SubscriptionFundingOutOfPlayForRequest(ctx) || req.ForceModel != "" || memberSelectedModel(ctx, selected.Model) {
		return nil
	}
	if _, explicit := catalog.ByID(req.RequestedModel); explicit {
		return nil
	}
	if selected.Metadata == nil {
		return nil
	}
	return candidates.SubscriptionAlternatives(req, selected, s.strategyPorts.Facts.ClassifierTelemetryOf(selected.Metadata).SelectionTrace, func(provider, model string) bool {
		return s.supportsSubscriptionTransport(provider) && credentials.ManagedSubscriptionCanServe(ctx, provider, model)
	})
}

func linkedSubscriptionResetAt(snapshot usage.Snapshot, now time.Time) time.Time {
	var resetAt time.Time
	for _, window := range []usage.Window{snapshot.Primary, snapshot.Secondary} {
		if window.UsedPercent < 0.999 {
			continue
		}
		candidate := window.ResetAt
		if candidate.IsZero() && window.WindowMinutes > 0 {
			observedAt := window.ObservedAt
			if observedAt.IsZero() {
				observedAt = snapshot.ObservedAt
			}
			candidate = observedAt.Add(time.Duration(window.WindowMinutes) * time.Minute)
		}
		if candidate.After(resetAt) {
			resetAt = candidate
		}
	}
	if !resetAt.After(now) {
		return now.Add(time.Minute)
	}
	return resetAt
}

func subscriptionCredentialFallbackUsed(ctx context.Context) bool {
	state, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*credentials.ManagedSubscriptionUsage)
	return state != nil && state.Finished && state.SubscriptionAttempted && (state.WinningCredentials == nil || !state.WinningCredentials.OAuth)
}
