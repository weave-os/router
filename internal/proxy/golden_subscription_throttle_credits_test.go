package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

var goldenClaudeSeatLongCooldown = goldenEpoch.Add(15 * time.Minute)

// withLongThrottledClaudeSeats cools every shared Claude seat for a provider
// rate limit far past the short seat wait.
func withLongThrottledClaudeSeats(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	claude := newGoldenSubscriptionPool(h.clock.Now, "claude-seat-a", "claude-seat-b")
	claude.cooling["claude-seat-a"], claude.cooling["claude-seat-b"] = goldenClaudeSeatLongCooldown, goldenClaudeSeatLongCooldown
	return svc.WithManagedSubscriptions(goldenProviderPools{subscriptions.ProviderClaude: claude})
}

// goldenNoLoginLongThrottledClaudeSeatsCtx is a Codex CLI caller with no usable
// personal login (its own Claude seat is disabled) whose installation's shared
// Claude seats are all rate-limited past the short wait.
func goldenNoLoginLongThrottledClaudeSeatsCtx(ctx context.Context) context.Context {
	models := []string{goldenAnthropicModel}
	ctx = requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{ClientApp: requestcontext.ClientAppCodex, SessionID: "golden-codex-session"})
	ctx = proxy.WithSubscriptionStateModels(ctx, models, models)
	cooldown := goldenClaudeSeatLongCooldown
	ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{
		{ID: "claude-seat-a", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
		{ID: "claude-seat-b", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooldown},
		{ID: "claude-own-seat", Provider: auth.SubscriptionProviderClaude, Enabled: false, State: auth.SubscriptionAccountStateDisabled},
	})
	return credentials.WithManagedSubscriptionUsage(ctx)
}

func goldenNoLoginLongThrottledClaudeSeatsScenario(suffix string, forced bool) goldenScenario {
	headers := goldenCodexSessionHeadersFor(suffix)
	name := ingressResponses + "_stream_no_login_long_claude_seat_throttle_funds_credits"
	if forced {
		headers[requestcontext.ForceModelHeader] = goldenAnthropicModel
		name = ingressResponses + "_stream_forced_no_login_long_claude_seat_throttle_funds_credits"
	}
	return goldenScenario{
		name: name, ingress: ingressResponses,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(true, "served on weave credits")}},
		requests:  []goldenRequest{{body: goldenCodexPortableToolTurn, headers: headers, decorateContext: goldenNoLoginLongThrottledClaudeSeatsCtx}},
		configure: withLongThrottledClaudeSeats,
	}
}

// TestNoLoginLongThrottledSeatsFundsCredits states the invariant: when every
// shared seat stays rate-limited past the short wait and the caller has no
// usable personal login, the turn is paid with Weave credits instead of
// failing, whether the model was routed or forced.
func TestNoLoginLongThrottledSeatsFundsCredits(t *testing.T) {
	t.Parallel()
	for _, sc := range []goldenScenario{
		goldenNoLoginLongThrottledClaudeSeatsScenario("c0e1", false),
		goldenNoLoginLongThrottledClaudeSeatsScenario("c0e2", true),
	} {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			h := newGoldenHarness(t, sc)
			requireServedOnCredits(t, h.serve(t, sc.ingress, sc.requests[0]), "served on weave credits")
		})
	}
}

// TestNoLoginLongThrottledSeatsCreditsDeniedIsRefused states the
// invariant: an installation that cannot pay with Weave credits is refused and
// never billed.
func TestNoLoginLongThrottledSeatsCreditsDeniedIsRefused(t *testing.T) {
	t.Parallel()
	sc := goldenNoLoginLongThrottledClaudeSeatsScenario("c0e3", false)
	sc.requests[0].decorateContext = chainCtx(goldenNoLoginLongThrottledClaudeSeatsCtx, goldenCreditsDeniedCtx)
	h := newGoldenHarness(t, sc)
	served := h.serve(t, sc.ingress, sc.requests[0])
	for _, attempt := range served.Attempts {
		require.NotEqual(t, "ok", attempt.Outcome, "Weave credits are off: no attempt may be served")
	}
	require.Empty(t, served.Billing, "a refused turn must not be billed")
	raw, err := json.Marshal(served.Response)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "served on weave credits")
}

func requireServedOnCredits(t *testing.T, served goldenRequestTrace, text string) {
	t.Helper()
	require.Nil(t, served.DispatchError, "dispatch error: %+v", served.DispatchError)
	require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
	raw, err := json.Marshal(served.Response)
	require.NoError(t, err)
	require.Contains(t, string(raw), text)
	require.NotEmpty(t, served.Attempts)
	last := served.Attempts[len(served.Attempts)-1]
	require.Equal(t, providers.ProviderAnthropic, last.Provider)
	require.Equal(t, "ok", last.Outcome)
	require.Empty(t, last.Credential, "the turn must be served on the deployment key, not a seat")
	require.Len(t, served.Billing, 1, "a credit-funded turn is debited exactly once")
	debit, ok := served.Billing[0].(map[string]any)
	require.True(t, ok, "billing record: %T", served.Billing[0])
	delta, ok := debit["DeltaUsdMicros"].(int64)
	require.True(t, ok, "billing record: %+v", debit)
	require.Negative(t, delta, "the turn must spend Weave credits, not settle free")
}
