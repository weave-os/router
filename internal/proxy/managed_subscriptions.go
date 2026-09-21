package proxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
)

// ManagedSubscriptionProvidersContextKey carries provider pools enrolled for
// the authenticated Router key. Values describe enrollment, not availability,
// so disabled or cooling accounts still fail closed instead of spending credits.
type ManagedSubscriptionProvidersContextKey struct{}

// ManagedSubscriptionEnrollmentUnavailableContextKey marks a request whose
// enrollment snapshot could not be loaded. Control-plane handlers remain
// available, while inference fails closed before any paid upstream dispatch.
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

// subscriptionOwnerFromContext resolves the pool this request draws from. It
// falls back to the authenticated key so a request authenticated before the
// owner was resolved still reaches its legacy accounts and nothing else.
func subscriptionOwnerFromContext(ctx context.Context) auth.SubscriptionOwner {
	if owner, ok := ctx.Value(SubscriptionOwnerContextKey{}).(auth.SubscriptionOwner); ok && owner.Valid() {
		return owner
	}
	return auth.SubscriptionOwner{APIKeyID: apiKeyIDFromContext(ctx)}
}

// ManagedSubscriptionUsage is request-local attribution shared by the auth
// middleware context and provider-specific dispatch attempt contexts.
type ManagedSubscriptionUsage struct {
	Served           bool
	CredentialSource string
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
	return context.WithValue(ctx, ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{})
}

func managedSubscriptionProviderFromUpstream(provider, model string) (subscriptions.Provider, bool) {
	switch provider {
	case providers.ProviderAnthropic:
		return subscriptions.ProviderClaude, true
	case providers.ProviderOpenAI:
		if codexSubscriptionCoversModel(model) {
			return subscriptions.ProviderCodex, true
		}
	}
	return "", false
}

func managedSubscriptionEnrolled(ctx context.Context, provider subscriptions.Provider) bool {
	if subscriptionRoutingDisabledForRequest(ctx) {
		return false
	}
	enrolled, _ := ctx.Value(ManagedSubscriptionProvidersContextKey{}).(map[auth.SubscriptionProvider]struct{})
	_, ok := enrolled[auth.SubscriptionProvider(provider)]
	return ok
}

func managedSubscriptionCanServe(ctx context.Context, provider, model string) bool {
	poolProvider, eligible := managedSubscriptionProviderFromUpstream(provider, model)
	return eligible && managedSubscriptionEnrolled(ctx, poolProvider)
}

func managedSubscriptionEnrollmentUnavailable(ctx context.Context) bool {
	unavailable, _ := ctx.Value(ManagedSubscriptionEnrollmentUnavailableContextKey{}).(bool)
	return unavailable
}

func markManagedSubscriptionServed(ctx context.Context, credentialCtx context.Context) {
	usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	if usage != nil {
		usage.Served = true
		if creds := CredentialsFromContext(credentialCtx); creds != nil {
			usage.CredentialSource = creds.Source
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

func (s *Service) leaseManagedSubscription(ctx context.Context, provider, model string) (context.Context, subscriptions.Lease, bool, error) {
	poolProvider, eligible := managedSubscriptionProviderFromUpstream(provider, model)
	if !eligible || s.managedSubscriptions == nil {
		return ctx, subscriptions.Lease{}, false, nil
	}
	if managedSubscriptionEnrollmentUnavailable(ctx) {
		return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolUnavailable
	}
	currentCredentials := CredentialsFromContext(ctx)
	if !managedSubscriptionEnrolled(ctx, poolProvider) || (currentCredentials != nil && currentCredentials.OAuth) {
		return ctx, subscriptions.Lease{}, false, nil
	}
	if subscriptionPlanAwareRoutingEnabled(ctx) && managedSubscriptionPlansAllExhausted(ctx) {
		if billing.SubscriptionOnlyFromContext(ctx) {
			return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolExhausted
		}
		return ctx, subscriptions.Lease{}, false, nil
	}
	owner := subscriptionOwnerFromContext(ctx)
	sessionID := ClientIdentityFrom(ctx).SessionID
	rejected := make([]subscriptions.Lease, 0, 1)
	defer func() {
		for _, skipped := range rejected {
			skipped.Release()
		}
	}()
	seen := make(map[string]struct{})
	var lease subscriptions.Lease
	for {
		var present bool
		var err error
		lease, present, err = s.managedSubscriptions.Lease(ctx, owner, poolProvider, sessionID)
		if err != nil {
			if errors.Is(err, subscriptions.ErrNoAvailableAccount) {
				if len(rejected) > 0 && !billing.SubscriptionOnlyFromContext(ctx) && s.managedProviderFallbackAvailable(ctx, poolProvider) {
					return ctx, subscriptions.Lease{}, false, nil
				}
				if len(rejected) > 0 {
					return ctx, subscriptions.Lease{}, true, anthropicSubscriptionModelUnavailable(model)
				}
				return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolExhausted
			}
			return ctx, subscriptions.Lease{}, present, errors.Join(ErrSubscriptionPoolUnavailable, err)
		}
		if !present && len(rejected) > 0 && !billing.SubscriptionOnlyFromContext(ctx) && s.managedProviderFallbackAvailable(ctx, poolProvider) {
			return ctx, subscriptions.Lease{}, false, nil
		}
		if !present && len(rejected) > 0 {
			return ctx, subscriptions.Lease{}, true, anthropicSubscriptionModelUnavailable(model)
		}
		if !present {
			return ctx, subscriptions.Lease{}, true, ErrSubscriptionPoolExhausted
		}
		if !s.subscriptionModels.managedDenied(owner.PoolKey(), lease.AccountID, provider, model, s.clockNow()) {
			snapshot, observed := s.managedSubscriptionUsageSnapshot(lease.AccessToken)
			if observed && snapshot.ExhaustedAsOf(s.clockNow()) {
				resetAt := linkedSubscriptionResetAt(snapshot, s.clockNow())
				if err := exhaustManagedSubscription(ctx, s.managedSubscriptions, owner, poolProvider, lease.AccountID, resetAt); err != nil {
					observability.FromContext(ctx).Error("Failed to persist exhausted subscription account",
						"provider", poolProvider, "account_id", lease.AccountID, "err", err)
				}
			} else {
				if coverage, covered := entitlement.CoverageFromContext(ctx); covered &&
					coverage.Plan == entitlement.PlanBoost &&
					!billing.SubscriptionOnlyFromContext(ctx) &&
					s.managedProviderFallbackAvailable(ctx, poolProvider) &&
					preferIncludedRouter(coverage, snapshot, observed, s.clockNow()) {
					lease.Release()
					observability.FromContext(ctx).Info("Boost source optimizer selected included Router capacity",
						"provider", poolProvider, "optimizer_version", subscriptionSourceOptimizerVersion)
					return ctx, subscriptions.Lease{}, false, nil
				}
				break
			}
		}
		if _, duplicate := seen[lease.AccountID]; duplicate {
			lease.Release()
			if !billing.SubscriptionOnlyFromContext(ctx) && s.managedProviderFallbackAvailable(ctx, poolProvider) {
				return ctx, subscriptions.Lease{}, false, nil
			}
			return ctx, subscriptions.Lease{}, true, anthropicSubscriptionModelUnavailable(model)
		}
		seen[lease.AccountID] = struct{}{}
		rejected = append(rejected, lease)
		sessionID = ""
	}
	// A leased access token is refreshed mid-session; the account it
	// authenticates is what an upstream decrypts reasoning under.
	creds := &Credentials{
		APIKey:      []byte(lease.AccessToken),
		OAuth:       true,
		Source:      credSourceSubscription,
		PrincipalID: "subscription-account:" + lease.AccountID,
	}
	if poolProvider == subscriptions.ProviderCodex {
		creds.Source = credSourceCodexSubscription
		creds.AccountID = []byte(lease.ProviderAccount)
		creds.PrincipalID = "chatgpt-account:" + lease.ProviderAccount
	}
	return context.WithValue(ctx, CredentialsContextKey{}, creds), lease, true, nil
}

func (s *Service) managedSubscriptionUsageSnapshot(accessToken string) (usage.Snapshot, bool) {
	if s.usageObserver == nil || accessToken == "" {
		return usage.Snapshot{}, false
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
		s.subscriptionModels.denyManaged(owner.PoolKey(), lease.AccountID, provider, model, s.clockNow().Add(subscriptionModelDenialTTL))
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
