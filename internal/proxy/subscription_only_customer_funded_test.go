package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A turn whose organization cannot pay (credits depleted or a spend cap) still
// serves on every customer-funded option before it is refused: the caller's
// own plan, then their provider key in its funding-priority order. Weave's
// deployment keys never fund it.

func goldenSpendCapCtx(ctx context.Context) context.Context {
	return billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlySpendCapReached)
}

func serveCustomerFunded(t *testing.T, c credentialCase) goldenRequestTrace {
	t.Helper()
	sc := c.scenario()
	h := newGoldenHarness(t, sc)
	trace := h.serve(t, sc.ingress, sc.requests[0])
	for _, attempt := range trace.Attempts {
		require.NotNil(t, attempt.Resolved, "credential scenarios record every attempt's credential")
		assert.NotEqual(t, "deployment", attempt.Resolved.Label, "a Weave deployment key funded a %s attempt", attempt.Provider)
	}
	return trace
}

func requireServedOnBYOK(t *testing.T, trace goldenRequestTrace) {
	t.Helper()
	require.Nil(t, trace.DispatchError, "turn was refused")
	require.NotEmpty(t, trace.Attempts)
	last := trace.Attempts[len(trace.Attempts)-1]
	assert.Equal(t, "byok_anthropic", last.Resolved.Label)
	assert.Equal(t, "ok", last.Outcome)
	require.Len(t, trace.Billing, 1)
	row, err := json.Marshal(trace.Billing[0])
	require.NoError(t, err)
	assert.Contains(t, string(row), `"FeeEntryType":"byok_fee"`, "a BYOK-served turn bills only the BYOK fee")
}

func TestSubscriptionOnly_KeyFirstBYOKServes(t *testing.T) {
	for name, withFunding := range map[string]func(context.Context) context.Context{
		"credits_depleted":  goldenSubscriptionOnlyCtx,
		"spend_cap_reached": goldenSpendCapCtx,
	} {
		t.Run(name, func(t *testing.T) {
			trace := serveCustomerFunded(t, credentialCase{
				name: "key_first", supplies: []string{goldenSubscription, goldenBYOKAnthropic},
				decisions: []router.Decision{goldenAnthropicDecision}, upstreams: anthropicUp(anthropicOK(false, "ok")),
				ctx: chainCtx(withFunding, goldenSubscriptionCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityKeyFirst))),
			})
			requireServedOnBYOK(t, trace)
			assert.Len(t, trace.Attempts, 1, "a key_first key is spent before the plan")
		})
	}
}

func TestSubscriptionOnly_PlanFirstBYOKServesAfterPlanExhausted(t *testing.T) {
	trace := serveCustomerFunded(t, credentialCase{
		name: "plan_first_exhausted", supplies: []string{goldenSubscription, goldenBYOKAnthropic},
		decisions: []router.Decision{goldenAnthropicDecision}, upstreams: anthropicUp(anthropicOK(false, "ok")),
		ctx:       chainCtx(goldenSubscriptionOnlyCtx, goldenSubscriptionCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityPlanFirst))),
		configure: exhaustSubscription,
	})
	requireServedOnBYOK(t, trace)
}

func TestSubscriptionOnly_PlanFirstBYOKLeavesThePlanFirst(t *testing.T) {
	trace := serveCustomerFunded(t, credentialCase{
		name: "plan_first_fresh", supplies: []string{goldenSubscription, goldenBYOKAnthropic},
		decisions: []router.Decision{goldenAnthropicDecision}, upstreams: anthropicUp(anthropicOK(false, "ok")),
		ctx: chainCtx(goldenSubscriptionOnlyCtx, goldenSubscriptionCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityPlanFirst))),
	})
	require.Nil(t, trace.DispatchError)
	require.Len(t, trace.Attempts, 1)
	assert.Equal(t, "subscription_context", trace.Attempts[0].Resolved.Label)
}

func TestSubscriptionOnly_NoCustomerKeyStillRefused(t *testing.T) {
	trace := serveCustomerFunded(t, credentialCase{
		name: "no_customer_key", supplies: []string{goldenSubscription}, refused: true,
		decisions: []router.Decision{goldenAnthropicDecision}, upstreams: anthropicUp(anthropicOK(false, "ok")),
		ctx: chainCtx(goldenSubscriptionOnlyCtx, goldenSubscriptionCtx), configure: exhaustSubscription,
	})
	require.NotNil(t, trace.DispatchError)
	assert.Equal(t, http.StatusPaymentRequired, trace.DispatchError.Status)
	assert.Empty(t, trace.Attempts)
}

func TestSubscriptionOnly_UnusableBYOKStillRefused(t *testing.T) {
	failed := byokKey(providers.ProviderAnthropic, "", auth.FundingPriorityKeyFirst)
	failed.CredentialError = errGoldenCredential
	trace := serveCustomerFunded(t, credentialCase{
		name: "unusable_key", supplies: []string{goldenSubscription}, refused: true,
		decisions: []router.Decision{goldenAnthropicDecision}, upstreams: anthropicUp(anthropicOK(false, "ok")),
		ctx: chainCtx(goldenSubscriptionOnlyCtx, goldenSubscriptionCtx, withBYOK(failed)), configure: exhaustSubscription,
	})
	require.NotNil(t, trace.DispatchError)
	assert.Equal(t, http.StatusPaymentRequired, trace.DispatchError.Status)
	assert.Empty(t, trace.Attempts)
}

func TestSubscriptionOnly_FailedBYOKNeverFallsBackToDeployment(t *testing.T) {
	trace := serveCustomerFunded(t, credentialCase{
		name: "byok_overloaded", supplies: []string{goldenSubscription, goldenBYOKAnthropic},
		decisions: []router.Decision{goldenRescueDecision, goldenCompatDecision},
		upstreams: map[string][]goldenStep{
			providers.ProviderAnthropic: {goldenUpstreamError(529, goldenOverloadBody)},
			providers.ProviderFireworks: {compatOK(false, "rescued")}},
		ctx: chainCtx(goldenSubscriptionOnlyCtx, goldenSubscriptionCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityKeyFirst))),
	})
	require.NotEmpty(t, trace.Attempts, "the customer's key must be tried")
	assert.Equal(t, "byok_anthropic", trace.Attempts[0].Resolved.Label)
	for _, attempt := range trace.Attempts {
		assert.Equal(t, providers.ProviderAnthropic, attempt.Provider, "no Weave-funded rescue")
	}
	assert.Empty(t, trace.Billing, "an unserved turn writes no ledger row")
}

func TestSubscriptionOnly_SpendCapNoticeNamesTheKeyNotThePlan(t *testing.T) {
	trace := serveCustomerFunded(t, credentialCase{
		name: "spend_cap_notice", stream: true, supplies: []string{goldenSubscription, goldenBYOKAnthropic},
		decisions: []router.Decision{goldenAnthropicDecision}, upstreams: anthropicUp(anthropicOK(true, "ok")),
		ctx: chainCtx(goldenSpendCapCtx, goldenSubscriptionCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityKeyFirst))),
	})
	require.Nil(t, trace.DispatchError)
	body, err := json.Marshal(trace.Response)
	require.NoError(t, err)
	assert.Contains(t, string(body), "spend cap", "the cap notice still shows")
	assert.NotContains(t, string(body), "your own Anthropic subscription", "the notice must not claim the plan served a BYOK turn")
}
