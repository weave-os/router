package proxy_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

const goldenCodexCallerRateLimitBody = `{"detail":"Rate limit exceeded"}`

const goldenLinkedCodexAccount = "golden-provider-codex-seat"

// goldenCallerCodexJWT is an unsigned Codex access token naming the caller's
// ChatGPT user, so the router can match it to the caller's own linked seat.
var goldenCallerCodexJWT = "eyJhbGciOiJub25lIn0." +
	base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_user_id":"golden-codex-user"}}`)) + ".signature"

// withConcurrentlyThrottledCodexSeat enrolls one linked Codex seat that a
// concurrent turn rate-limited after this request loaded its admission
// snapshot, so the pool can no longer lease it.
func withConcurrentlyThrottledCodexSeat(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	codex := newGoldenSubscriptionPool(h.clock.Now, "codex-seat")
	codex.cooling["codex-seat"] = goldenEpoch.Add(40 * time.Second)
	return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenProviderPools{subscriptions.ProviderCodex: codex})
}

// withLeasableLinkedCodexSeat enrolls one linked Codex seat the pool would lease.
func withLeasableLinkedCodexSeat(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenProviderPools{subscriptions.ProviderCodex: newGoldenSubscriptionPool(h.clock.Now, "codex-seat")})
}

// withSameAndOtherAccountCodexSeats enrolls the caller's own linked seat and a
// teammate's seat in the same ChatGPT workspace.
func withSameAndOtherAccountCodexSeats(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenProviderPools{subscriptions.ProviderCodex: newGoldenSubscriptionPool(h.clock.Now, "codex-seat", "codex-seat-other")})
}

// goldenCallerCodexLinkedSeatCtx presents the caller's own Codex login on a
// linked-first turn. The caller's ChatGPT user also owns a linked Codex
// seat whose admission state is active with no quota snapshot.
func goldenCallerCodexLinkedSeatCtx(seats ...*auth.SubscriptionAccount) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, proxy.OpenAISubscriptionContextKey{}, goldenCallerCodexJWT)
		ctx = context.WithValue(ctx, proxy.OpenAIAccountIDContextKey{}, goldenLinkedCodexAccount)
		ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyOwnSeatFirst: true}})
		ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
		ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "golden-codex-session"})
		ctx = proxy.WithSubscriptionStateModels(ctx, []string{goldenCodexModel}, []string{goldenCodexModel})
		ctx = proxy.WithManagedSubscriptionCandidates(ctx, append([]*auth.SubscriptionAccount{
			{ID: "codex-seat", Provider: auth.SubscriptionProviderCodex, ExternalAccountID: goldenLinkedCodexAccount, ProviderUserID: "golden-codex-user", Enabled: true, State: auth.SubscriptionAccountStateActive},
		}, seats...))
		return proxy.WithManagedSubscriptionUsage(ctx)
	}
}

func goldenCreditsOffCtx(decorate func(context.Context) context.Context) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return billing.WithWeaveCreditsDenied(decorate(ctx))
	}
}

func goldenForcedCodexHeaders(suffix string) map[string]string {
	headers := goldenCodexSessionHeadersFor(suffix)
	headers[proxy.ForceModelHeader] = goldenCodexModel
	return headers
}

// goldenCodexCallerThrottleScenarios pin a turn forced onto a Codex model
// whose caller-presented Codex login is rate-limited while its linked seat
// cannot serve: Weave credits pay for the turn, unless credits are off.
func goldenCodexCallerThrottleScenarios() []goldenScenario {
	forced := []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel)}
	throttledThenCredits := map[string][]goldenStep{providers.ProviderOpenAI: {
		goldenUpstreamError(http.StatusTooManyRequests, goldenCodexCallerRateLimitBody),
		goldenResponsesOK("served on weave credits"),
	}}
	teammateSeat := &auth.SubscriptionAccount{ID: "codex-seat-other", Provider: auth.SubscriptionProviderCodex, ExternalAccountID: goldenLinkedCodexAccount, ProviderUserID: "golden-codex-teammate", Enabled: true, State: auth.SubscriptionAccountStateActive}
	return []goldenScenario{{
		name: ingressResponses + "_stream_forced_codex_caller_throttled_seat_leased_out_serves_on_credits", ingress: ingressResponses,
		decisions: forced, upstreams: throttledThenCredits,
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e1"), decorateContext: goldenCallerCodexLinkedSeatCtx()}},
		configure: withConcurrentlyThrottledCodexSeat,
	}, {
		name: ingressResponses + "_stream_forced_codex_caller_throttled_seat_leased_out_credits_off_refused", ingress: ingressResponses,
		decisions: forced, upstreams: throttledThenCredits,
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e2"), decorateContext: goldenCreditsOffCtx(goldenCallerCodexLinkedSeatCtx())}},
		configure: withConcurrentlyThrottledCodexSeat,
	}, {
		name: ingressResponses + "_stream_forced_codex_caller_throttled_same_account_seat_serves_on_credits", ingress: ingressResponses,
		decisions: forced, upstreams: throttledThenCredits,
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e3"), decorateContext: goldenCallerCodexLinkedSeatCtx()}},
		configure: withLeasableLinkedCodexSeat,
	}, {
		name: ingressResponses + "_stream_forced_codex_caller_throttled_same_account_seat_credits_off_refused", ingress: ingressResponses,
		decisions: forced, upstreams: throttledThenCredits,
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e4"), decorateContext: goldenCreditsOffCtx(goldenCallerCodexLinkedSeatCtx())}},
		configure: withLeasableLinkedCodexSeat,
	}, {
		name: ingressResponses + "_stream_forced_codex_caller_throttled_teammate_seat_serves", ingress: ingressResponses,
		decisions: forced, upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
			goldenUpstreamError(http.StatusTooManyRequests, goldenCodexCallerRateLimitBody),
			goldenResponsesOK("served on the other seat"),
		}},
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e5"), decorateContext: goldenCallerCodexLinkedSeatCtx(teammateSeat)}},
		configure: withSameAndOtherAccountCodexSeats,
	}}
}

// goldenCreditDebits counts the billing entries that draw down Weave credits.
func goldenCreditDebits(t *testing.T, served goldenRequestTrace) int {
	t.Helper()
	debits := 0
	for _, entry := range served.Billing {
		raw, err := json.Marshal(entry)
		require.NoError(t, err)
		if gjson.GetBytes(raw, "DeltaUsdMicros").Int() < 0 {
			debits++
		}
	}
	return debits
}

func goldenRawTrace(t *testing.T, served goldenRequestTrace) string {
	t.Helper()
	raw, err := json.Marshal(served)
	require.NoError(t, err)
	return string(raw)
}

// TestRateLimitedCallerCodexLoginFallsBackToCredits states the invariant the
// scenarios pin: once the caller's own Codex login is rate-limited and no
// linked seat can be leased, the forced turn is served on Weave credits and
// debited exactly once; with credits off it is refused and never billed.
func TestRateLimitedCallerCodexLoginFallsBackToCredits(t *testing.T) {
	t.Parallel()
	scenarios := goldenCodexCallerThrottleScenarios()
	for _, index := range []int{0, 2} {
		sc := scenarios[index]
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			served := newGoldenHarness(t, sc).serve(t, sc.ingress, sc.requests[0])
			raw := goldenRawTrace(t, served)
			require.Contains(t, raw, "served on weave credits")
			require.NotContains(t, raw, "subscription_pool_unavailable")
			last := served.Attempts[len(served.Attempts)-1]
			require.Equal(t, "ok", last.Outcome)
			require.Empty(t, last.Credential, "the rescue is served on the deployment key, not a subscription")
			require.Len(t, served.Attempts, 2, "only the caller's login and the paid rescue are attempted; the rate-limited account is not retried")
			require.Equal(t, 1, goldenCreditDebits(t, served), "a turn served on Weave credits is debited exactly once")
		})
	}
	for _, index := range []int{1, 3} {
		sc := scenarios[index]
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			served := newGoldenHarness(t, sc).serve(t, sc.ingress, sc.requests[0])
			raw := goldenRawTrace(t, served)
			require.NotContains(t, raw, "served on weave credits")
			for _, attempt := range served.Attempts {
				require.NotEqual(t, "ok", attempt.Outcome)
			}
			require.Zero(t, goldenCreditDebits(t, served), "a refused turn is never billed")
		})
	}
}

// TestRateLimitedCallerCodexLoginPrefersTeammateSeat states the invariant: a
// teammate's seat in the caller's ChatGPT workspace has its own rate limit, so
// it still serves the turn before Weave credits are spent.
func TestRateLimitedCallerCodexLoginPrefersTeammateSeat(t *testing.T) {
	t.Parallel()
	sc := goldenCodexCallerThrottleScenarios()[4]
	served := newGoldenHarness(t, sc).serve(t, sc.ingress, sc.requests[0])
	require.Contains(t, goldenRawTrace(t, served), "served on the other seat")
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, "ok", last.Outcome)
	require.Equal(t, "codex_subscription", last.Credential)
	require.Len(t, served.Attempts, 2)
	require.Zero(t, goldenCreditDebits(t, served), "a turn served on a linked seat draws no Weave credits")
}
