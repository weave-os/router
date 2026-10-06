package proxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/subscriptions"
)

// ManagedSubscriptionProvidersContextKey carries provider pools enrolled for
// the authenticated Router key. Values describe enrollment, not availability,
// so disabled or cooling accounts still fail closed instead of spending credits.
type ManagedSubscriptionProvidersContextKey struct{}

// ManagedSubscriptionEnrollmentUnavailableContextKey marks a request whose
// enrollment snapshot could not be loaded. Codex OpenAI turns may use their
// configured API credential when paid fallback is permitted; other subscription
// lanes and subscription-only requests fail closed.
type ManagedSubscriptionEnrollmentUnavailableContextKey struct{}

// ManagedSubscriptionUsageContextKey carries per-request billing attribution.
type ManagedSubscriptionUsageContextKey struct{}

// SubscriptionOwnerContextKey carries the linked-account owner resolved by the
// auth middleware.
type SubscriptionOwnerContextKey struct{}

// WithSubscriptionOwner records which linked accounts this request may serve.
func WithSubscriptionOwner(ctx context.Context, owner auth.SubscriptionOwner) context.Context {
	return context.WithValue(ctx, SubscriptionOwnerContextKey{}, owner)
}

// subscriptionOwnerFromContext resolves request identity for primary admission.
// Enrollment key identity never partitions serving capacity.
func subscriptionOwnerFromContext(ctx context.Context) auth.SubscriptionOwner {
	if _, ok := requestcontext.InternalTestIdentityFrom(ctx); ok {
		return auth.SubscriptionOwner{}
	}
	if owner, ok := ctx.Value(SubscriptionOwnerContextKey{}).(auth.SubscriptionOwner); ok && owner.Valid() {
		return owner
	}
	return auth.SubscriptionOwner{APIKeyID: apiKeyIDFromContext(ctx)}
}

// ManagedSubscriptionUsage is request-local attribution shared by the auth
// middleware context and provider-specific dispatch attempt contexts.
type ManagedSubscriptionUsage struct {
	Served                bool
	SubscriptionAttempted bool
	CredentialSource      string
	OverageInUse          bool
	SubscriptionAccountID string
	SubscriptionOwnerID   string
	SubscriptionTier      auth.SubscriptionTier
	IntendedModel         string
	AttemptedAccounts     map[string]struct{}
	Finished              bool
	WinningCredentials    *Credentials
}

var (
	ErrSubscriptionPoolExhausted   = errors.New("subscription account pool exhausted")
	ErrSubscriptionPoolUnavailable = errors.New("subscription account pool unavailable")
)

func isSubscriptionPoolError(err error) bool {
	return errors.Is(err, ErrSubscriptionPoolExhausted) || errors.Is(err, ErrSubscriptionPoolUnavailable)
}

// WithManagedSubscriptionUsage prepares request-local subscription attribution.
func WithManagedSubscriptionUsage(ctx context.Context) context.Context {
	if existing, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); existing != nil {
		return ctx
	}
	return context.WithValue(ctx, ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{})
}

func managedSubscriptionProviderFromUpstream(provider, model string) (subscriptions.Provider, bool) {
	switch provider {
	case providers.ProviderAnthropic:
		return subscriptions.ProviderClaude, true
	case providers.ProviderOpenAI:
		if codexSubscriptionCanAttemptModel(model) {
			return subscriptions.ProviderCodex, true
		}
	}
	return "", false
}

func managedSubscriptionEnrolled(ctx context.Context, provider subscriptions.Provider) bool {
	if subscriptionAPIOnly(ctx) || subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	enrolled, _ := ctx.Value(ManagedSubscriptionProvidersContextKey{}).(map[auth.SubscriptionProvider]struct{})
	_, ok := enrolled[auth.SubscriptionProvider(provider)]
	return ok
}

func managedSubscriptionCanServe(ctx context.Context, provider, model string) bool {
	if provider == providers.ProviderOpenAI && codexChatEndpoint(ctx) {
		return false
	}
	poolProvider, eligible := managedSubscriptionProviderFromUpstream(provider, model)
	return eligible && managedSubscriptionEnrolled(ctx, poolProvider)
}

func managedSubscriptionEnrollmentUnavailable(ctx context.Context) bool {
	unavailable, _ := ctx.Value(ManagedSubscriptionEnrollmentUnavailableContextKey{}).(bool)
	return unavailable
}

func (s *Service) markManagedSubscriptionServed(ctx context.Context, credentialCtx context.Context, leases ...subscriptions.Lease) {
	var lease subscriptions.Lease
	if len(leases) > 0 {
		lease = leases[0]
	}
	usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if usage != nil {
		usage.Served = true
		usage.SubscriptionAccountID = lease.AccountID
		usage.SubscriptionOwnerID = lease.OwnerID
		usage.SubscriptionTier = lease.Tier
		if creds := CredentialsFromContext(credentialCtx); creds != nil {
			usage.CredentialSource = creds.Source
			if s.usageObserver != nil && creds.Source == credSourceSubscription {
				if snapshot, observed := s.usageObserver.Snapshot(s.usageObserver.Key(creds.APIKey)); observed {
					usage.OverageInUse = snapshot.OverageInUse
				}
			}
		}
	}
}

func managedSubscriptionCredentialSource(ctx context.Context) string {
	usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if usage == nil || !usage.Served {
		return ""
	}
	return usage.CredentialSource
}

func managedSubscriptionServed(ctx context.Context) bool {
	usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return usage != nil && usage.Served
}

func (s *Service) subscriptionOverageInUse(ctx context.Context) bool {
	if !servedOnSubscription(ctx) {
		return false
	}
	if creds := CredentialsFromContext(ctx); creds != nil && creds.Source == credSourceCodexSubscription {
		return false
	}
	if managedSubscriptionCredentialSource(ctx) == credSourceCodexSubscription {
		return false
	}
	headers := UnifiedLimitHeadersFrom(ctx)
	if claim, present := headers["anthropic-ratelimit-unified-representative-claim"]; present {
		return usage.PaidAnthropicOverage(
			usage.AnthropicClaim(claim), headers["anthropic-ratelimit-unified-overage-in-use"])
	}
	if managedUsage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); managedUsage != nil && managedUsage.Served {
		return managedUsage.OverageInUse
	}
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.Source != credSourceSubscription || s.usageObserver == nil {
		return false
	}
	snapshot, observed := s.usageObserver.Snapshot(s.usageObserver.Key(creds.APIKey))
	return observed && snapshot.OverageInUse
}

func (s *Service) costNeutralSubscriptionServed(ctx context.Context) bool {
	return servedOnSubscription(ctx) && !s.subscriptionOverageInUse(ctx)
}

func (s *Service) leaseManagedSubscription(ctx context.Context, provider, model string) (context.Context, subscriptions.Lease, bool, error) {
	poolProvider, eligible := managedSubscriptionProviderFromUpstream(provider, model)
	if eligible && managedSubscriptionEnrollmentUnavailable(ctx) {
		if credentials := CredentialsFromContext(ctx); credentials != nil && credentials.OAuth && s.includedOnlySubscriptionTransport(provider) {
			return ctx, subscriptions.Lease{}, false, nil
		}
		if subscriptionAttemptOnly(ctx) || paidFallbackForbidden(ctx) || !s.managedProviderFallbackAvailable(ctx, poolProvider) {
			return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolUnavailable
		}
		if credentials := CredentialsFromContext(ctx); credentials != nil && credentials.OAuth {
			if poolProvider == subscriptions.ProviderClaude {
				ctx = withSuppressedClaudeSubscription(ctx)
			} else {
				ctx = withSuppressedCodexSubscription(ctx)
			}
			ctx = resolveAndInjectCredentials(ctx, provider, model, http.Header{})
		}
		return ctx, subscriptions.Lease{}, false, nil
	}
	if subscriptionAttemptOnly(ctx) && (!eligible || !s.includedOnlySubscriptionTransport(provider) || !managedSubscriptionEnrolled(ctx, poolProvider)) && !servedOnSubscription(ctx) {
		return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolExhausted
	}
	if eligible && (claudeSubscriptionSuppressed(ctx) && provider == providers.ProviderAnthropic || codexSubscriptionSuppressed(ctx) && provider == providers.ProviderOpenAI) && !managedSubscriptionEnrolled(ctx, poolProvider) && !s.managedProviderFallbackAvailable(ctx, poolProvider) {
		return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolExhausted
	}
	if !eligible || s.managedSubscriptions == nil || !managedSubscriptionEnrolled(ctx, poolProvider) ||
		poolProvider == subscriptions.ProviderCodex && codexChatEndpoint(ctx) {
		return ctx, subscriptions.Lease{}, false, nil
	}
	if !s.includedOnlySubscriptionTransport(provider) {
		return ctx, subscriptions.Lease{}, false, nil
	}
	if current := CredentialsFromContext(ctx); current != nil && current.OAuth {
		return ctx, subscriptions.Lease{}, false, nil
	}

	owner := subscriptionOwnerFromContext(ctx)
	if winner, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); winner != nil {
		for pair := range winner.AttemptedAccounts {
			if strings.HasSuffix(pair, "\x00"+model) {
				owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, strings.TrimSuffix(pair, "\x00"+model))
			}
		}
	}
	seen := make(map[string]bool)
	var modelDenial error
	for {
		lease, present, err := s.managedSubscriptions.Lease(ctx, owner, poolProvider, ClientIdentityFrom(ctx).SessionID)
		if err != nil || !present {
			if !subscriptionAttemptOnly(ctx) && !paidFallbackForbidden(ctx) && s.managedProviderFallbackAvailable(ctx, poolProvider) {
				return ctx, subscriptions.Lease{}, false, nil
			}
			if err != nil && !errors.Is(err, subscriptions.ErrNoAvailableAccount) {
				return ctx, subscriptions.Lease{}, present, errors.Join(ErrSubscriptionPoolUnavailable, err)
			}
			if modelDenial != nil {
				return ctx, subscriptions.Lease{}, true, modelDenial
			}
			return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolExhausted
		}
		if seen[lease.AccountID] {
			lease.Release()
			if !subscriptionAttemptOnly(ctx) && !paidFallbackForbidden(ctx) && s.managedProviderFallbackAvailable(ctx, poolProvider) {
				return ctx, subscriptions.Lease{}, false, nil
			}
			if modelDenial != nil {
				return ctx, subscriptions.Lease{}, true, modelDenial
			}
			return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolExhausted
		}
		seen[lease.AccountID] = true
		if s.subscriptionModels.managedDenied("account:"+lease.AccountID, lease.AccountID, provider, model, s.clockNow()) {
			if poolProvider == subscriptions.ProviderCodex {
				modelDenial = codexSubscriptionModelUnavailable(model)
			} else {
				modelDenial = anthropicSubscriptionModelUnavailable(model)
			}
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		snapshot, observed := s.managedSubscriptionUsageSnapshot(lease.AccountID, lease.AccessToken)
		if observed && snapshot.OverageInUse {
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		if observed && snapshot.ExhaustedAsOf(s.clockNow()) {
			resetAt := linkedSubscriptionResetAt(snapshot, s.clockNow())
			if err := exhaustManagedSubscription(ctx, s.managedSubscriptions, owner, poolProvider, lease.AccountID, resetAt); err != nil {
				observability.FromContext(ctx).Error("Failed to persist exhausted subscription account", "provider", poolProvider, "account_id", lease.AccountID, "err", err)
			}
			lease.Release()
			owner.ExcludedAccountIDs = append(owner.ExcludedAccountIDs, lease.AccountID)
			continue
		}
		creds := &Credentials{APIKey: []byte(lease.AccessToken), OAuth: true, Source: credSourceSubscription, PrincipalID: "subscription-account:" + lease.AccountID, SubscriptionAccountID: lease.AccountID}
		if poolProvider == subscriptions.ProviderCodex {
			creds.Source = credSourceCodexSubscription
			creds.AccountID = []byte(lease.ProviderAccount)
			creds.PrincipalID = "chatgpt-account:" + lease.ProviderAccount
		}
		return context.WithValue(ctx, CredentialsContextKey{}, creds), lease, true, nil
	}
}

func (s *Service) managedSubscriptionUsageSnapshot(accountID, accessToken string) (usage.Snapshot, bool) {
	if s.usageObserver == nil || accessToken == "" {
		return usage.Snapshot{}, false
	}
	if accountID != "" {
		if snapshot, found := s.usageObserver.Snapshot(s.usageObserver.Key([]byte("subscription-account:" + accountID))); found {
			return snapshot, true
		}
	}
	return s.usageObserver.Snapshot(s.usageObserver.Key([]byte(accessToken)))
}

func (s *Service) managedProviderFallbackAvailable(ctx context.Context, provider subscriptions.Provider) bool {
	switch provider {
	case subscriptions.ProviderClaude:
		return s.anthropicFallbackKeyAvailable(ctx)
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
	poolProvider, eligible := managedSubscriptionProviderFromUpstream(provider, model)
	if !eligible || lease.AccountID == "" {
		return false
	}
	status := upstreamStatus(attemptErr)
	owner := subscriptionOwnerFromContext(ctx)
	if provider == providers.ProviderAnthropic && anthropicSubscriptionModelRejected(attemptErr) {
		s.subscriptionModels.denyManaged("account:"+lease.AccountID, lease.AccountID, provider, model, s.clockNow().Add(subscriptionModelDenialTTL))
		observability.FromContext(ctx).Warn("Managed subscription account cannot access model",
			"provider", poolProvider, "account_id", lease.AccountID, "model", model)
		return true
	}
	if provider == providers.ProviderOpenAI && codexSubscriptionModelRejected(attemptErr) {
		s.subscriptionModels.denyManaged("account:"+lease.AccountID, lease.AccountID, provider, model, s.clockNow().Add(subscriptionModelDenialTTL))
		observability.FromContext(ctx).Warn("Managed subscription account cannot access model",
			"provider", poolProvider, "account_id", lease.AccountID, "model", model)
		return true
	}
	switch status {
	case http.StatusTooManyRequests:
		resetAt := managedSubscriptionResetAt(attemptErr, s.clockNow())
		if err := s.managedSubscriptions.Cooldown(ctx, owner, poolProvider, lease.AccountID, resetAt); err != nil {
			observability.FromContext(ctx).Error("Failed to persist subscription account cooldown",
				"provider", poolProvider, "account_id", lease.AccountID, "err", err)
		}
		observability.FromContext(ctx).Warn("Subscription account quota exhausted",
			"provider", poolProvider, "account_id", lease.AccountID, "cooldown_until", resetAt)
		return true
	case http.StatusUnauthorized, http.StatusForbidden:
		if err := reconnectManagedSubscription(ctx, s.managedSubscriptions, owner, poolProvider, lease.AccountID); err != nil {
			observability.FromContext(ctx).Error("Failed to disable rejected subscription account",
				"provider", poolProvider, "account_id", lease.AccountID, "err", err)
		}
		observability.FromContext(ctx).Warn("Subscription account credential rejected",
			"provider", poolProvider, "account_id", lease.AccountID, "upstream_status", status)
		return true
	default:
		return false
	}
}

func (s *Service) recordManagedSubscriptionSuccess(ctx context.Context, provider, model string, lease subscriptions.Lease) {
	if lease.AccountID == "" || lease.State == auth.SubscriptionAccountStateActive {
		return
	}
	poolProvider, eligible := managedSubscriptionProviderFromUpstream(provider, model)
	if !eligible {
		return
	}
	health, ok := s.managedSubscriptions.(interface {
		Activate(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string) error
	})
	if !ok {
		return
	}
	if err := health.Activate(ctx, subscriptionOwnerFromContext(ctx), poolProvider, lease.AccountID); err != nil {
		observability.FromContext(ctx).Error("Failed to mark subscription account active",
			"provider", poolProvider, "account_id", lease.AccountID, "err", err)
	}
}

func reconnectManagedSubscription(ctx context.Context, leaser subscriptions.Leaser, owner auth.SubscriptionOwner, provider subscriptions.Provider, accountID string) error {
	if health, ok := leaser.(interface {
		ReconnectRequired(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string) error
	}); ok {
		return health.ReconnectRequired(ctx, owner, provider, accountID)
	}
	return leaser.Disable(ctx, owner, provider, accountID)
}

func managedSubscriptionResetAt(err error, now time.Time) time.Time {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) {
		return now.Add(time.Minute)
	}
	if retryAfter := upstream.Headers.Get("Retry-After"); retryAfter != "" {
		if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
			return now.Add(time.Duration(seconds) * time.Second)
		}
		if resetAt, parseErr := http.ParseTime(retryAfter); parseErr == nil && resetAt.After(now) {
			return resetAt
		}
	}
	return now.Add(time.Minute)
}

func (s *Service) includedOnlySubscriptionTransport(provider string) bool {
	client, err := s.clients.Client(provider)
	if err != nil {
		return false
	}
	transport, ok := client.(providers.IncludedOnlySubscriptionTransport)
	return ok && transport.IncludedOnlySubscriptions()
}

// subscriptionAlternativeDecisions uses only the policy's request-compatible
// ordering in its selected quality group. Explicit choices remain fixed.
func (s *Service) subscriptionAlternativeDecisions(ctx context.Context, req router.Request, selected router.Decision) []router.Decision {
	if subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) || req.ForceModel != "" {
		return nil
	}
	if _, explicit := catalog.ByID(req.RequestedModel); explicit {
		return nil
	}
	if selected.Metadata == nil {
		return nil
	}
	var ordered []string
	if trace := selected.Metadata.SelectionTrace; trace != nil && trace.SelectedGroup != "" {
		ordered = trace.EffectiveOrders[trace.SelectedGroup]
	}
	if len(ordered) == 0 {
		ordered = selected.Metadata.RescueModels
	}
	if len(ordered) == 0 {
		ordered = selected.Metadata.CandidateModels
	}
	selectedEntry, selectedKnown := catalog.ByID(selected.Model)
	if !selectedKnown {
		return nil
	}
	var alternatives []router.Decision
	seen := map[string]bool{selected.Model: true}
	for _, arm := range ordered {
		model, effort := hmm.SplitEffort(arm)
		model = hmm.CatalogIDForRoster(model)
		eligible := false
		for _, candidate := range selected.Metadata.CandidateModels {
			if candidate == model {
				eligible = true
			}
		}
		if trace := selected.Metadata.SelectionTrace; trace != nil {
			for _, candidate := range trace.CandidateRosterIDs {
				base, _ := hmm.SplitEffort(candidate)
				if hmm.CatalogIDForRoster(base) == model {
					eligible = true
				}
			}
			for _, rejection := range trace.ResolverExclusions {
				if rejection.CatalogID == model {
					eligible = false
					break
				}
			}
		}
		if !eligible {
			continue
		}
		if seen[model] {
			continue
		}
		seen[model] = true
		if _, blocked := req.ExcludedModels[model]; blocked {
			continue
		}
		if _, blocked := req.AutomaticExcludedModels[model]; blocked {
			continue
		}
		if _, blocked := req.SafetyExcludedModels[model]; blocked {
			continue
		}
		if _, blocked := req.UnsignedHistoryExcludedModels[model]; blocked {
			continue
		}
		entry, known := catalog.ByID(model)
		if !known || entry.Tier < selectedEntry.Tier || req.HasTools && (entry.ToolUseQuality == catalog.ToolUseLow || entry.AgenticUse == catalog.AgenticLow) || req.HasImages && entry.ImageInput == catalog.ImageInputUnsupported {
			continue
		}
		if entry.ContextWindow > 0 && req.EstimatedInputTokens > entry.ContextWindow {
			if _, admitted := req.OverflowAdmittedModels[model]; !admitted {
				continue
			}
		}
		for _, binding := range entry.Providers {
			if req.EnabledProviders != nil {
				if _, enabled := req.EnabledProviders[binding.Provider]; !enabled {
					continue
				}
			}
			if !s.includedOnlySubscriptionTransport(binding.Provider) || !managedSubscriptionCanServe(ctx, binding.Provider, model) {
				continue
			}
			alternatives = append(alternatives, router.Decision{Model: model, Provider: binding.Provider, Effort: effort, Metadata: selected.Metadata, Reason: selected.Reason})
			break
		}
	}
	return alternatives
}

func linkedSubscriptionResetAt(snapshot usage.Snapshot, now time.Time) time.Time {
	var resetAt time.Time
	for _, window := range []usage.Window{snapshot.Primary, snapshot.Secondary} {
		if window.UsedPercent < 0.999 {
			continue
		}
		candidate := window.ResetAt
		if candidate.IsZero() && window.WindowMinutes > 0 {
			candidate = snapshot.ObservedAt.Add(time.Duration(window.WindowMinutes) * time.Minute)
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
	state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return state != nil && state.Finished && state.SubscriptionAttempted && (state.WinningCredentials == nil || !state.WinningCredentials.OAuth)
}
