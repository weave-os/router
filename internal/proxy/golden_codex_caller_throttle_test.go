package proxy_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
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

// withGoldenCodexSeats enrolls a Codex pool with the given seats; a seat in
// cooling was rate-limited by a concurrent turn after this request loaded its
// admission snapshot, so the pool can no longer lease it.
func withGoldenCodexSeats(cooling map[string]time.Time, seats ...string) func(*proxy.Service, *goldenHarness) *proxy.Service {
	return func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		codex := newGoldenSubscriptionPool(h.clock.Now, seats...)
		maps.Copy(codex.cooling, cooling)
		return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
			WithManagedSubscriptions(goldenProviderPools{subscriptions.ProviderCodex: codex})
	}
}

var (
	goldenOwnCodexSeat      = &auth.SubscriptionAccount{ID: "codex-seat", Provider: auth.SubscriptionProviderCodex, ExternalAccountID: goldenLinkedCodexAccount, ProviderUserID: "golden-codex-user", Enabled: true, State: auth.SubscriptionAccountStateActive}
	goldenTeammateCodexSeat = &auth.SubscriptionAccount{ID: "codex-seat-other", Provider: auth.SubscriptionProviderCodex, ExternalAccountID: goldenLinkedCodexAccount, ProviderUserID: "golden-codex-teammate", Enabled: true, State: auth.SubscriptionAccountStateActive}
)

// goldenCallerCodexSeatsCtx presents the caller's own Codex login on a
// linked-first turn whose linked seats are active in the admission snapshot
// with no quota snapshot.
func goldenCallerCodexSeatsCtx(seats ...*auth.SubscriptionAccount) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, credentials.OpenAISubscriptionContextKey{}, goldenCallerCodexJWT)
		ctx = context.WithValue(ctx, credentials.OpenAIAccountIDContextKey{}, goldenLinkedCodexAccount)
		ctx = flags.WithOverrides(ctx, flags.Overrides{Bools: map[flags.Key]bool{flags.KeyOwnSeatFirst: true}})
		ctx = billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyLinkedFirst)
		ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "golden-codex-session"})
		ctx = proxy.WithSubscriptionStateModels(ctx, []string{goldenCodexModel}, []string{goldenCodexModel})
		ctx = proxy.WithManagedSubscriptionCandidates(ctx, seats)
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
	coolingTeammateSeat := withGoldenCodexSeats(map[string]time.Time{goldenTeammateCodexSeat.ID: goldenEpoch.Add(40 * time.Second)}, goldenTeammateCodexSeat.ID)
	ownSeat := withGoldenCodexSeats(nil, goldenOwnCodexSeat.ID)
	return []goldenScenario{{
		name: ingressResponses + "_stream_forced_codex_caller_throttled_seat_leased_out_serves_on_credits", ingress: ingressResponses,
		decisions: forced, upstreams: throttledThenCredits,
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e1"), decorateContext: goldenCallerCodexSeatsCtx(goldenTeammateCodexSeat)}},
		configure: coolingTeammateSeat,
	}, {
		name: ingressResponses + "_stream_forced_codex_caller_throttled_seat_leased_out_credits_off_refused", ingress: ingressResponses,
		decisions: forced, upstreams: throttledThenCredits,
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e2"), decorateContext: goldenCreditsOffCtx(goldenCallerCodexSeatsCtx(goldenTeammateCodexSeat))}},
		configure: coolingTeammateSeat,
	}, {
		name: ingressResponses + "_stream_forced_codex_caller_throttled_same_account_seat_serves_on_credits", ingress: ingressResponses,
		decisions: forced, upstreams: throttledThenCredits,
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e3"), decorateContext: goldenCallerCodexSeatsCtx(goldenOwnCodexSeat)}},
		configure: ownSeat,
	}, {
		name: ingressResponses + "_stream_forced_codex_caller_throttled_same_account_seat_credits_off_refused", ingress: ingressResponses,
		decisions: forced, upstreams: throttledThenCredits,
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e4"), decorateContext: goldenCreditsOffCtx(goldenCallerCodexSeatsCtx(goldenOwnCodexSeat))}},
		configure: ownSeat,
	}, {
		name: ingressResponses + "_stream_forced_codex_caller_throttled_teammate_seat_serves", ingress: ingressResponses,
		decisions: forced, upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
			goldenUpstreamError(http.StatusTooManyRequests, goldenCodexCallerRateLimitBody),
			goldenResponsesOK("served on the other seat"),
		}},
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenForcedCodexHeaders("c1e5"), decorateContext: goldenCallerCodexSeatsCtx(goldenOwnCodexSeat, goldenTeammateCodexSeat)}},
		configure: withGoldenCodexSeats(nil, goldenOwnCodexSeat.ID, goldenTeammateCodexSeat.ID),
	}}
}

// goldenCreditDebits counts billing entries with a negative DeltaUsdMicros,
// i.e. Weave-credit debits.
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
