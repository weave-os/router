package proxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func eligibilityRefusalContexts() map[string]context.Context {
	contexts := make(map[string]context.Context)
	for name, tc := range subscriptionLaneRefusalCases() {
		contexts[name] = tc.ctx
		contexts[name+"+allowance-spent"] = entitlement.WithSpentWindow(tc.ctx, entitlement.SpentWindow{Kind: entitlement.PeriodKindWeekly})
	}
	return contexts
}

func TestClaudeEnterpriseRefusalOutranksEveryLaneMark(t *testing.T) {
	target := inference.Target{Provider: providers.ProviderAnthropic, CatalogID: catalog.ModelIDClaudeOpus48.String()}
	for name, ctx := range eligibilityRefusalContexts() {
		t.Run(name, func(t *testing.T) {
			err := claudeEnterpriseFunding(enterpriseOnlyContext(ctx), target, false)

			require.ErrorIs(t, err, ErrClaudeEnterpriseSubscriptionUnavailable)
			assert.Equal(t, refusal.ClaudeEnterpriseOnly.Contract(), classifiedRefusal(t, err))
		})
	}
}

func TestClientAppRefusalOutranksEveryLaneRefusal(t *testing.T) {
	for name, ctx := range eligibilityRefusalContexts() {
		t.Run(name, func(t *testing.T) {
			for _, laneErr := range []error{subscriptionPoolExhausted(ctx), credentials.SubscriptionOnlyUnavailable(ctx)} {
				err := errors.Join(ErrSubscriptionClientAppRefused, laneErr)

				assert.Equal(t, refusal.SubscriptionClientAppRefused.Contract(), classifiedRefusal(t, err))
			}
		})
	}
}

func TestProductIneligibilityOutranksEveryLaneRefusal(t *testing.T) {
	ineligible := catalog.CheckEligibility(eligibility.MaxOpenSourceOnly, catalog.ModelIDClaudeOpus48.String())
	require.ErrorIs(t, ineligible, eligibility.ErrModelIneligible)
	for name, ctx := range eligibilityRefusalContexts() {
		t.Run(name, func(t *testing.T) {
			for _, laneErr := range []error{subscriptionPoolExhausted(ctx), credentials.SubscriptionOnlyUnavailable(ctx), ErrSubscriptionClientAppRefused, ErrClaudeEnterpriseSubscriptionUnavailable} {
				assert.Equal(t, refusal.ProductIneligible.Contract(), classifiedRefusal(t, errors.Join(ineligible, laneErr)))
			}
		})
	}
}

func TestDispatchRefusesAnIneligibleDecisionUnderEveryLaneMark(t *testing.T) {
	for name, ctx := range eligibilityRefusalContexts() {
		t.Run(name, func(t *testing.T) {
			anthropic := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("served")}}}
			svc := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderAnthropic: anthropic})
			rec := httptest.NewRecorder()
			in := plannedInputs(ctx, rec, newPreludeBuffer(rec), []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}}, nil)
			in.InitialDecision = router.Decision{Model: catalog.ModelIDClaudeOpus48.String(), Provider: providers.ProviderAnthropic}

			_, err := svc.dispatchWithFallback(entitlement.WithProductScope(ctx, entitlement.PlanMax), in)

			require.ErrorIs(t, err, eligibility.ErrModelIneligible)
			assert.Equal(t, refusal.ProductIneligible.Contract(), classifiedRefusal(t, err))
			assert.Zero(t, anthropic.calls)
		})
	}
}

func TestEligibilityRefusalSentinelsClassifyAsTheirRefusedState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		refuse   func(context.Context) error
		refused  subscriptionlane.State
		sentinel error
	}{
		{name: "client app", refuse: subscriptionClientAppRefusal, refused: subscriptionlane.RefusedSubscriptionClientApp, sentinel: ErrSubscriptionClientAppRefused},
		{name: "claude enterprise", refuse: claudeEnterpriseRefusal, refused: subscriptionlane.RefusedClaudeEnterpriseOnly, sentinel: ErrClaudeEnterpriseSubscriptionUnavailable},
	} {
		kind, refused := tc.refused.Refusal()
		require.True(t, refused, tc.name)
		assert.Equal(t, kind.Contract(), classifiedRefusal(t, tc.sentinel), tc.name)
		assert.Equal(t, tc.sentinel, tc.refuse(context.Background()), tc.name)
	}
}
