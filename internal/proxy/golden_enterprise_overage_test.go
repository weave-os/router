package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
)

var errGoldenProfileTimeout = errors.New("golden: subscription profile lookup timed out")

type goldenFailingClaudePlan struct{}

func (goldenFailingClaudePlan) SubscriptionPlan(context.Context, []byte) (providers.SubscriptionPlan, error) {
	return providers.SubscriptionPlanUnknown, errGoldenProfileTimeout
}

// goldenEnterpriseOverageSnapshot is what an Enterprise seat reports on every
// allowed turn: paid overage in use and no included 5h or 7d window.
var goldenEnterpriseOverageSnapshot = usage.Snapshot{RepresentativeClaim: usage.AnthropicClaimOverage, OverageInUse: true}

// goldenPlanOverageSnapshot is a Pro/Max login that spent its included windows
// and is now drawing paid extra usage.
var goldenPlanOverageSnapshot = usage.Snapshot{
	Primary:             usage.Window{UsedPercent: 1.0, WindowMinutes: 300},
	Secondary:           usage.Window{UsedPercent: 0.4, WindowMinutes: 10080},
	RepresentativeClaim: usage.AnthropicClaimOverage, OverageInUse: true,
}

func observedOverage(snap usage.Snapshot, plans providers.SubscriptionPlanReader) func(*proxy.Service, *goldenHarness) *proxy.Service {
	return func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
		obs := usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now)
		obs.Record(obs.Key([]byte(goldenSubscription)), snap)
		return svc.WithUsageObserver(obs).WithClaudeSubscriptionPlanReader(plans)
	}
}

// goldenEnterpriseOverageScenarios pin a caller whose own Claude login reports
// paid overage while its plan lookup fails.
func goldenEnterpriseOverageScenarios() []goldenScenario {
	decisions := []router.Decision{goldenAnthropicDecision}
	cases := []credentialCase{
		{name: "enterprise_overage_plan_lookup_failed_credits_off_served_on_own_login", supplies: []string{goldenSubscription}, stream: true,
			decisions: decisions, upstreams: anthropicUp(anthropicOK(true, "served on own login")),
			ctx: chainCtx(goldenSubscriptionCtx, goldenCreditsDeniedCtx), configure: observedOverage(goldenEnterpriseOverageSnapshot, goldenFailingClaudePlan{})},
		{name: "enterprise_overage_plan_lookup_failed_credits_on_served_on_own_login", supplies: []string{goldenSubscription}, stream: true,
			decisions: decisions, upstreams: anthropicUp(anthropicOK(true, "served on own login")),
			ctx: goldenSubscriptionCtx, configure: observedOverage(goldenEnterpriseOverageSnapshot, goldenFailingClaudePlan{})},
		{name: "plan_overage_plan_lookup_failed_credits_off_served_on_own_login", supplies: []string{goldenSubscription}, stream: true,
			decisions: decisions, upstreams: anthropicUp(anthropicOK(true, "served on own login")),
			ctx: chainCtx(goldenSubscriptionCtx, goldenCreditsDeniedCtx), configure: observedOverage(goldenPlanOverageSnapshot, goldenFailingClaudePlan{})},
		{name: "max_plan_overage_credits_on_paid_fallback", supplies: []string{goldenSubscription}, stream: true,
			decisions: decisions, upstreams: anthropicUp(anthropicOK(true, "served on weave credits")),
			ctx: goldenSubscriptionCtx, configure: observedOverage(goldenPlanOverageSnapshot, goldenClaudePlan(providers.SubscriptionPlanMax))},
	}
	scenarios := make([]goldenScenario, len(cases))
	for i, c := range cases {
		scenarios[i] = c.scenario()
	}
	return scenarios
}

// TestOwnClaudeLoginOverageFunding states the invariant the scenarios pin:
// overage on the caller's own login is dropped for a paid fallback only when
// the plan is known not to be Enterprise and Weave credits can fund the turn.
func TestOwnClaudeLoginOverageFunding(t *testing.T) {
	t.Parallel()
	scenarios := goldenEnterpriseOverageScenarios()
	for i, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			h := newGoldenHarness(t, sc)
			served := h.serve(t, sc.ingress, sc.requests[0])
			if i == len(scenarios)-1 {
				requireServedOnCredits(t, served, "served on weave credits")
				return
			}
			require.Nil(t, served.DispatchError, "dispatch error: %+v", served.DispatchError)
			require.Equal(t, http.StatusOK, served.Response.Status, "response: %+v", served.Response.Body)
			require.Len(t, served.Attempts, 1)
			require.Equal(t, "subscription", served.Attempts[0].Credential)
			require.NotNil(t, served.Attempts[0].Resolved)
			require.Equal(t, "subscription_context", served.Attempts[0].Resolved.Label, "the caller's own login funds the turn")
			require.Equal(t, "ok", served.Attempts[0].Outcome)
		})
	}
}
