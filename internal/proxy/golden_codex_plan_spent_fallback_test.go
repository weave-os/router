package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

const goldenCodexSiblingModel = "gpt-5.6-terra"

func goldenCodexSessionHeadersFor(suffix string) map[string]string {
	return map[string]string{"User-Agent": goldenCodexUserAgent, "Session-Id": "00000000-0000-4000-8000-00000000" + suffix}
}

var goldenCodexSeatCooldown = goldenEpoch.Add(8 * time.Second)

// withThrottledCodexSeatsAndClaudeSeat cools both Codex seats for a short
// provider rate limit and leaves one Claude seat free.
func withThrottledCodexSeatsAndClaudeSeat(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	codex := newGoldenSubscriptionPool(h.clock.Now, "codex-seat-a", "codex-seat-b")
	codex.cooling["codex-seat-a"], codex.cooling["codex-seat-b"] = goldenCodexSeatCooldown, goldenCodexSeatCooldown
	return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenProviderPools{
			subscriptions.ProviderCodex:  codex,
			subscriptions.ProviderClaude: newGoldenSubscriptionPool(h.clock.Now, "claude-seat"),
		})
}

// withOnlyThrottledCodexSeats cools both Codex seats for a short provider rate
// limit and enrolls no Claude pool, so the seats are the only included option.
func withOnlyThrottledCodexSeats(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	codex := newGoldenSubscriptionPool(h.clock.Now, "codex-seat-a", "codex-seat-b")
	codex.cooling["codex-seat-a"], codex.cooling["codex-seat-b"] = goldenCodexSeatCooldown, goldenCodexSeatCooldown
	return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenProviderPools{subscriptions.ProviderCodex: codex})
}

func goldenSpentCodexLoginOnlyThrottledSeatsCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel}
	ctx = goldenCodexCtx(ctx)
	ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	cooldown := goldenCodexSeatCooldown
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat-a", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
		{ID: "codex-seat-b", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

// goldenShortSeatThrottleScenarios pin a Codex turn whose own plan is spent
// while every shared Codex seat sits in a short provider rate-limit cooldown:
// the turn waits out the cooldown and serves on a seat.
func goldenShortSeatThrottleScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressResponses + "_stream_spent_codex_login_waits_out_short_seat_throttle", ingress: ingressResponses,
		decisions: []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel)},
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenResponsesOK("served on codex seat after cooldown"),
			},
		},
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0d1"), decorateContext: goldenSpentCodexLoginOnlyThrottledSeatsCtx}},
		configure: withOnlyThrottledCodexSeats,
	}}
}

// TestSpentCodexLoginWaitsOutShortSeatThrottle states the invariant: a seat
// rate limit that clears within seconds is waited out, and the turn is served
// on the seat rather than refused.
func TestSpentCodexLoginWaitsOutShortSeatThrottle(t *testing.T) {
	t.Parallel()
	sc := goldenShortSeatThrottleScenarios()[0]
	h := newGoldenHarness(t, sc)
	served := h.serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, "ok", last.Outcome)
	require.Equal(t, providers.ProviderOpenAI, last.Provider)
	raw, err := json.Marshal(served.Response)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "Connect or reconnect a Codex seat")
}

func goldenSpentCodexLoginThrottledSeatsCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel, goldenCodexSiblingModel, goldenAnthropicModel}
	ctx = goldenCodexCtx(ctx)
	ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	cooldown := goldenCodexSeatCooldown
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat-a", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
		{ID: "codex-seat-b", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

func goldenCodexSiblingAndClaudeSeatsCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel, goldenCodexSiblingModel, goldenAnthropicModel}
	ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
		{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

// goldenCodexPlanSpentFallbackScenarios pin a Codex turn whose plan hit its
// usage limit: the limit covers every Codex model, so the turn moves straight to
// the Claude seat instead of retrying sibling Codex models on the spent plan.
func goldenCodexPlanSpentFallbackScenarios() []goldenScenario {
	return []goldenScenario{{
		name: ingressResponses + "_stream_codex_plan_usage_limit_skips_sibling_codex_models", ingress: ingressResponses,
		decisions: []router.Decision{
			decision(providers.ProviderOpenAI, goldenCodexModel),
			decision(providers.ProviderOpenAI, goldenCodexSiblingModel),
			decision(providers.ProviderAnthropic, goldenAnthropicModel),
		},
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
			},
			providers.ProviderAnthropic: {anthropicOK(true, "served on claude seat")},
		},
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0d2"), decorateContext: goldenCodexSiblingAndClaudeSeatsCtx}},
		configure: withCodexAndClaudeSeats,
	}, {
		name: ingressResponses + "_stream_spent_codex_login_throttled_seats_skips_sibling_codex_models", ingress: ingressResponses,
		decisions: []router.Decision{
			decision(providers.ProviderOpenAI, goldenCodexModel),
			decision(providers.ProviderOpenAI, goldenCodexSiblingModel),
			decision(providers.ProviderAnthropic, goldenAnthropicModel),
		},
		upstreams: map[string][]goldenStep{
			providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
				goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
			},
			providers.ProviderAnthropic: {anthropicOK(true, "served on claude seat")},
		},
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0d3"), decorateContext: goldenSpentCodexLoginThrottledSeatsCtx}},
		configure: withThrottledCodexSeatsAndClaudeSeat,
	}}
}

// TestSpentCodexLoginSkipsSiblingCodexModels states the invariant the
// scenarios pin: a Codex usage limit spends the whole plan, so no sibling Codex
// model is attempted on it before the Claude seat serves.
func TestSpentCodexLoginSkipsSiblingCodexModels(t *testing.T) {
	t.Parallel()
	for _, sc := range goldenCodexPlanSpentFallbackScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			h := newGoldenHarness(t, sc)
			served := h.serve(t, sc.ingress, sc.requests[0])
			require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
			for _, attempt := range served.Attempts {
				require.NotEqual(t, goldenCodexSiblingModel, attempt.Model, "sibling Codex model attempted on the spent plan")
			}
			last := served.Attempts[len(served.Attempts)-1]
			require.Equal(t, providers.ProviderAnthropic, last.Provider)
			require.Equal(t, "ok", last.Outcome)
		})
	}
}

var goldenCodexSeatLongCooldown = goldenEpoch.Add(2 * time.Minute)

func withLongThrottledCodexSeats(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	codex := newGoldenSubscriptionPool(h.clock.Now, "codex-seat-a", "codex-seat-b")
	codex.cooling["codex-seat-a"], codex.cooling["codex-seat-b"] = goldenCodexSeatLongCooldown, goldenCodexSeatLongCooldown
	return svc.WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)).
		WithManagedSubscriptions(goldenProviderPools{subscriptions.ProviderCodex: codex})
}

func goldenSpentCodexLoginLongThrottledSeatsCtx(ctx context.Context) context.Context {
	models := []string{goldenCodexModel}
	ctx = goldenCodexCtx(ctx)
	ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	cooldown := goldenCodexSeatLongCooldown
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "codex-seat-a", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
		{ID: "codex-seat-b", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
	})
	return proxy.WithManagedSubscriptionUsage(ctx)
}

// TestSpentCodexLoginThrottledSeatsNoticeNamesSeats states the invariant: when
// the caller's own plan is spent, the shared seats are rate-limited past the
// wait and credits are denied, the notice names the rate-limited seats instead of asking the caller to
// reconnect a seat.
func TestSpentCodexLoginThrottledSeatsNoticeNamesSeats(t *testing.T) {
	t.Parallel()
	sc := goldenScenario{
		name: ingressResponses + "_stream_spent_codex_login_long_seat_throttle_notice", ingress: ingressResponses,
		decisions: []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel)},
		upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
			goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
			goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
		}},
		requests: []goldenRequest{
			{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0d4"), decorateContext: goldenSpentCodexLoginLongThrottledSeatsCreditsDeniedCtx},
			{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0d4"), decorateContext: goldenSpentCodexLoginLongThrottledSeatsCreditsDeniedCtx, advance: time.Second},
		},
		configure: withLongThrottledCodexSeats,
	}
	h := newGoldenHarness(t, sc)
	h.serve(t, sc.ingress, sc.requests[0])
	served := h.serve(t, sc.ingress, sc.requests[1])
	raw, err := json.Marshal(served.Response)
	require.NoError(t, err)
	body := string(raw)
	require.NotContains(t, body, "Connect or reconnect a Codex seat")
	require.Contains(t, body, "rate-limited")
}

func goldenSpentCodexLoginLongThrottledSeatsCreditsDeniedCtx(ctx context.Context) context.Context {
	return goldenCreditsDeniedCtx(goldenSpentCodexLoginLongThrottledSeatsCtx(ctx))
}

// TestSpentCodexLoginLongThrottledSeatsFundsCredits states the invariant: when
// the caller's own plan is spent and every shared seat stays rate-limited past
// the short wait, the turn is paid with Weave credits instead of refused.
func TestSpentCodexLoginLongThrottledSeatsFundsCredits(t *testing.T) {
	t.Parallel()
	sc := goldenScenario{
		name: ingressResponses + "_stream_spent_codex_login_long_seat_throttle_funds_credits", ingress: ingressResponses,
		decisions: []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel)},
		upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
			goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
			goldenResponsesOK("served on weave credits"),
		}},
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: goldenCodexSessionHeadersFor("c0d5"), decorateContext: goldenSpentCodexLoginLongThrottledSeatsCtx}},
		configure: withLongThrottledCodexSeats,
	}
	h := newGoldenHarness(t, sc)
	served := h.serve(t, sc.ingress, sc.requests[0])
	raw, err := json.Marshal(served.Response)
	require.NoError(t, err)
	require.Contains(t, string(raw), "served on weave credits")
	require.NotContains(t, string(raw), "rate-limited")
}

// TestSpentCodexLoginPrefersFreeClaudeSeatOverSeatWait states the invariant: a
// free Claude seat serves the turn at once instead of the turn waiting out a
// short Codex seat cooldown and retrying the spent Codex model.
func TestSpentCodexLoginPrefersFreeClaudeSeatOverSeatWait(t *testing.T) {
	t.Parallel()
	sc := goldenCodexPlanSpentFallbackScenarios()[1]
	sc.requests[0].headers = goldenCodexSessionHeadersFor("c0d7")
	h := newGoldenHarness(t, sc)
	served := h.serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	var codexAttempts int
	for _, attempt := range served.Attempts {
		if attempt.Provider == providers.ProviderOpenAI {
			codexAttempts++
		}
	}
	require.Equal(t, 1, codexAttempts, "spent Codex model retried after a seat wait while a Claude seat was free")
	require.Equal(t, providers.ProviderAnthropic, served.Attempts[len(served.Attempts)-1].Provider)
}

// TestForcedSpentCodexTurnWaitsOutShortSeatThrottle states the invariant: a
// turn forced onto a Codex model cannot move to the free Claude seat, so it
// still waits out the short Codex seat cooldown instead of failing.
func TestForcedSpentCodexTurnWaitsOutShortSeatThrottle(t *testing.T) {
	t.Parallel()
	sc := goldenCodexPlanSpentFallbackScenarios()[1]
	sc.decisions = []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel)}
	sc.upstreams = map[string][]goldenStep{
		providers.ProviderOpenAI: {
			goldenUpstreamError(http.StatusTooManyRequests, goldenCodexModelUsageLimitBody),
			goldenResponsesOK("served on codex seat after cooldown"),
		},
	}
	sc.requests[0].headers = goldenCodexSessionHeadersFor("c0d8")
	sc.requests[0].headers[proxy.ForceModelHeader] = goldenCodexModel
	h := newGoldenHarness(t, sc)
	served := h.serve(t, sc.ingress, sc.requests[0])
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, providers.ProviderOpenAI, last.Provider)
	require.Equal(t, "ok", last.Outcome)
}
