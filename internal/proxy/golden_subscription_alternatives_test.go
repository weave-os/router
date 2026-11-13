package proxy_test

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

const (
	goldenCodexAlternativeModel = "gpt-6-sol"
	goldenModelDeniedBody       = `{"error":{"code":"model_not_found","message":"golden seat model unavailable"}}`
	goldenUsageLimitBody        = `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600}}`
	goldenAutoChatBody          = `{"model":"auto","stream":true,"messages":[{"role":"user","content":"Say hi"}]}`
	goldenAutoChatToolBody      = `{"model":"auto","stream":true,"messages":[{"role":"user","content":"Say hi"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
)

// goldenSubscriptionPool is a managed-subscription pool: it leases the first
// enabled account the owner has not excluded and that is not cooling down.
type goldenSubscriptionPool struct {
	mu       sync.Mutex
	now      func() time.Time
	accounts []string
	cooling  map[string]time.Time
	disabled map[string]bool
}

func newGoldenSubscriptionPool(now func() time.Time, accounts ...string) *goldenSubscriptionPool {
	return &goldenSubscriptionPool{now: now, accounts: accounts, cooling: map[string]time.Time{}, disabled: map[string]bool{}}
}

func (p *goldenSubscriptionPool) Lease(_ context.Context, owner auth.SubscriptionOwner, _ subscriptions.Provider, _ string) (subscriptions.Lease, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range p.accounts {
		if p.disabled[id] || p.cooling[id].After(p.now()) || slices.Contains(owner.ExcludedAccountIDs, id) {
			continue
		}
		return subscriptions.Lease{
			AccountID: id, OwnerID: "golden-owner", Tier: auth.SubscriptionTierPersonal,
			AccessToken: "golden-seat-" + id, ProviderAccount: "golden-provider-" + id,
		}, true, nil
	}
	return subscriptions.Lease{}, true, subscriptions.ErrNoAvailableAccount
}

func (p *goldenSubscriptionPool) Cooldown(_ context.Context, _ auth.SubscriptionOwner, _ subscriptions.Provider, accountID string, until time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cooling[accountID] = until
	return nil
}

func (p *goldenSubscriptionPool) Disable(_ context.Context, _ auth.SubscriptionOwner, _ subscriptions.Provider, accountID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disabled[accountID] = true
	return nil
}

func withGoldenCodexPool(accounts ...string) func(*proxy.Service, *goldenHarness) *proxy.Service {
	return func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		return withGoldenClock(svc, h).WithManagedSubscriptions(newGoldenSubscriptionPool(h.clock.Now, accounts...))
	}
}

func goldenCodexPoolCtx(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, proxy.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderCodex: {}})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

var goldenCodexAlternativeDecision = router.Decision{
	Provider: providers.ProviderOpenAI, Model: goldenCodexModel, Reason: "golden_route",
	Metadata: &router.RoutingMetadata{
		CandidateModels: []string{goldenCodexModel, goldenCodexAlternativeModel},
		SelectionTrace: &router.SelectionTrace{
			SelectedGroup:      "high",
			EffectiveOrders:    map[string][]string{"high": {goldenCodexModel, goldenCodexAlternativeModel}},
			CandidateRosterIDs: []string{goldenCodexModel, goldenCodexAlternativeModel},
		},
	},
}

// goldenSubscriptionAlternativeScenarios pin the walk across same-group
// alternatives on leased subscription seats before any paid key is used.
func goldenSubscriptionAlternativeScenarios() []goldenScenario {
	prefix := ingressOpenAI + "_stream_"
	requests := []goldenRequest{{body: goldenAutoChatBody, decorateContext: goldenCodexPoolCtx}}
	deniedThenServed := map[string][]goldenStep{providers.ProviderOpenAI: {
		goldenUpstreamError(http.StatusNotFound, goldenModelDeniedBody),
		goldenResponsesOK("on alternative seat")}}
	return []goldenScenario{
		{name: prefix + "subscription_alternative_after_seat_model_denied", ingress: ingressOpenAI,
			decisions: []router.Decision{goldenCodexAlternativeDecision},
			upstreams: deniedThenServed,
			requests:  requests, configure: withGoldenCodexPool("seat-a")},
		{name: prefix + "subscription_alternative_with_tools_after_seat_model_denied", ingress: ingressOpenAI,
			decisions: []router.Decision{goldenCodexAlternativeDecision},
			upstreams: deniedThenServed,
			requests:  []goldenRequest{{body: goldenAutoChatToolBody, decorateContext: goldenCodexPoolCtx}},
			configure: withGoldenCodexPool("seat-a")},
		{name: prefix + "subscription_seat_rotation_before_alternative", ingress: ingressOpenAI,
			decisions: []router.Decision{goldenCodexAlternativeDecision},
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenUsageLimitBody),
				goldenResponsesOK("on second seat")}},
			requests: requests, configure: withGoldenCodexPool("seat-a", "seat-b")},
		{name: prefix + "subscription_pool_spent_ends_turn_without_paid_fallback", ingress: ingressOpenAI,
			decisions: []router.Decision{goldenCodexAlternativeDecision},
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenUsageLimitBody),
				goldenResponsesOK("never served")}},
			requests: requests, configure: withGoldenCodexPool("seat-a")},
	}
}
