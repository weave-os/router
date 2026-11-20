package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions"
)

const goldenThirdPartyRefusalBody = `{"type":"error","error":{"type":"invalid_request_error","message":"Third-party apps now draw from your extra usage, not your plan limits. Add more at claude.ai/settings/usage and keep going."}}`

const goldenOverloadedBody = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`

var goldenClaudeSeatShortCooldown = goldenEpoch.Add(8 * time.Second)

func withClaudeSeat(cooldown time.Time) func(*proxy.Service, *goldenHarness) *proxy.Service {
	return func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		claude := newGoldenSubscriptionPool(h.clock.Now, "claude-seat")
		if !cooldown.IsZero() {
			claude.cooling["claude-seat"] = cooldown
		}
		return svc.WithManagedSubscriptions(goldenProviderPools{subscriptions.ProviderClaude: claude})
	}
}

func goldenClaudeSeatCtx(clientApp string, cooldown time.Time) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: clientApp, SessionID: "golden-seat-session"})
		seat := &auth.SubscriptionAccount{ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateActive}
		if !cooldown.IsZero() {
			seat.State, seat.CooldownUntil = auth.SubscriptionAccountStateCooldown, &cooldown
		}
		ctx = proxy.WithManagedSubscriptionCandidates(ctx, []*auth.SubscriptionAccount{seat})
		return proxy.WithManagedSubscriptionUsage(ctx)
	}
}

func stateLane(sc goldenScenario) goldenScenario {
	decorate := sc.requests[0].decorateContext
	models := []string{goldenAnthropicModel}
	sc.name += "_state_lane"
	sc.noCandidateErr = fmt.Errorf("hmm_embedding: arm selection: %w", policy.ErrNoEligibleArm)
	sc.requests[0].decorateContext = func(ctx context.Context) context.Context {
		return proxy.WithSubscriptionStateModels(decorate(ctx), models, models)
	}
	return sc
}

func goldenSeatRefusalScenario(name, clientApp string, cooldown time.Time, seatSteps ...goldenStep) goldenScenario {
	return goldenScenario{
		name: ingressAnthropic + "_stream_" + name, ingress: ingressAnthropic,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: append(seatSteps, anthropicOK(true, "served on weave credits"))},
		requests:  []goldenRequest{{body: goldenAnthropicBody(true), decorateContext: goldenClaudeSeatCtx(clientApp, cooldown)}},
		configure: withClaudeSeat(cooldown),
	}
}

func TestSeatRefusalsFundCredits(t *testing.T) {
	t.Parallel()
	forced := goldenSeatRefusalScenario("forced_client_app_refused_seat_funds_credits", proxy.ClientAppOpencode, time.Time{},
		goldenUpstreamError(http.StatusBadRequest, goldenThirdPartyRefusalBody))
	forced.requests[0].headers = map[string]string{proxy.ForceModelHeader: goldenAnthropicModel}
	for _, sc := range []goldenScenario{
		goldenSeatRefusalScenario("overloaded_seat_funds_credits", proxy.ClientAppClaudeCode, time.Time{},
			goldenUpstreamError(http.StatusServiceUnavailable, goldenOverloadedBody)),
		goldenSeatRefusalScenario("short_seat_throttle_outside_state_lane_funds_credits", proxy.ClientAppClaudeCode, goldenClaudeSeatShortCooldown),
		goldenSeatRefusalScenario("client_app_refused_seat_funds_credits", proxy.ClientAppOpencode, time.Time{},
			goldenUpstreamError(http.StatusBadRequest, goldenThirdPartyRefusalBody)),
		forced,
		stateLane(goldenSeatRefusalScenario("overloaded_seat_funds_credits", proxy.ClientAppClaudeCode, time.Time{},
			goldenUpstreamError(http.StatusServiceUnavailable, goldenOverloadedBody))),
		stateLane(goldenSeatRefusalScenario("client_app_refused_seat_funds_credits", proxy.ClientAppOpencode, time.Time{},
			goldenUpstreamError(http.StatusBadRequest, goldenThirdPartyRefusalBody))),
	} {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			h := newGoldenHarness(t, sc)
			requireServedOnCredits(t, h.serve(t, sc.ingress, sc.requests[0]), "served on weave credits")
		})
	}
}

func TestOverloadedSeatFundsCreditsOnNextTurn(t *testing.T) {
	t.Parallel()
	base := goldenSeatRefusalScenario("overloaded_seat_next_turn_funds_credits", proxy.ClientAppClaudeCode, time.Time{},
		goldenUpstreamError(http.StatusServiceUnavailable, goldenOverloadedBody))
	base.upstreams[providers.ProviderAnthropic] = append(base.upstreams[providers.ProviderAnthropic], anthropicOK(true, "next turn on weave credits"))
	base.noCandidateErr = fmt.Errorf("hmm_embedding: arm selection: %w", policy.ErrNoEligibleArm)
	next := base.requests[0]
	next.advance = time.Second
	base.requests = append(base.requests, next)
	for _, sc := range []goldenScenario{base, stateLane(base)} {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			h := newGoldenHarness(t, sc)
			requireServedOnCredits(t, h.serve(t, sc.ingress, sc.requests[0]), "served on weave credits")
			requireServedOnCredits(t, h.serve(t, sc.ingress, sc.requests[1]), "next turn on weave credits")
		})
	}
}

func TestSeatsWithoutQuotaSnapshotFundCredits(t *testing.T) {
	t.Parallel()
	sc := goldenSeatRefusalScenario("rate_limited_seat_without_quota_snapshot_funds_credits", proxy.ClientAppClaudeCode, time.Time{},
		goldenUpstreamError(http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limited"}}`))
	sc.requests[0].decorateContext = func(ctx context.Context) context.Context {
		ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode, SessionID: "golden-seat-session"})
		ctx = context.WithValue(ctx, proxy.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
		return proxy.WithManagedSubscriptionUsage(ctx)
	}
	h := newGoldenHarness(t, sc)
	requireServedOnCredits(t, h.serve(t, sc.ingress, sc.requests[0]), "served on weave credits")
}

type goldenUnknownClaudePlan struct{}

func (goldenUnknownClaudePlan) SubscriptionPlan(context.Context, []byte) (providers.SubscriptionPlan, error) {
	return providers.SubscriptionPlanUnknown, nil
}

func goldenCallerPlanScenario(name string, failure goldenStep) goldenScenario {
	return goldenScenario{
		name: ingressAnthropic + "_stream_" + name, ingress: ingressAnthropic,
		decisions: []router.Decision{decision(providers.ProviderAnthropic, goldenAnthropicModel)},
		upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {failure, anthropicOK(true, "served on weave credits")}},
		requests: []goldenRequest{{body: goldenAnthropicBody(true), decorateContext: func(ctx context.Context) context.Context {
			return proxy.WithManagedSubscriptionUsage(goldenSubscriptionCtx(ctx))
		}}},
	}
}

func TestTriedUnverifiedPlansFundCredits(t *testing.T) {
	t.Parallel()
	enterprise := goldenSeatRefusalScenario("unverified_claude_plan_seat_failure_funds_credits", proxy.ClientAppClaudeCode, time.Time{},
		goldenUpstreamError(http.StatusServiceUnavailable, goldenOverloadedBody))
	enterprise.noCandidateErr = fmt.Errorf("hmm_embedding: arm selection: %w", policy.ErrNoEligibleArm)
	configureSeat := enterprise.configure
	enterprise.configure = func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		return configureSeat(svc, h).WithClaudeSubscriptionPlanReader(goldenUnknownClaudePlan{})
	}
	thirdParty := goldenCallerPlanScenario("caller_plan_third_party_refusal_funds_credits",
		goldenUpstreamError(http.StatusBadRequest, goldenThirdPartyRefusalBody))
	thirdParty.requests[0].decorateContext = func(ctx context.Context) context.Context {
		ctx = requestcontext.WithClientIdentity(ctx, proxy.ClientIdentity{ClientApp: proxy.ClientAppOpencode, SessionID: "golden-seat-session"})
		return proxy.WithManagedSubscriptionUsage(goldenSubscriptionCtx(ctx))
	}
	for _, sc := range []goldenScenario{
		goldenCallerPlanScenario("caller_plan_model_denied_funds_credits",
			goldenUpstreamError(http.StatusNotFound, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-haiku-4-5"}}`)),
		thirdParty,
		enterprise,
		stateLane(enterprise),
	} {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			h := newGoldenHarness(t, sc)
			requireServedOnCredits(t, h.serve(t, sc.ingress, sc.requests[0]), "served on weave credits")
		})
	}
}
