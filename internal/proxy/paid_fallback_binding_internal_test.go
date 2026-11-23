package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptionlane"

	"github.com/stretchr/testify/assert"
)

type planBindingFacts struct {
	includedLane, apiOnly, enterpriseSeat, capRefused, ownPlan, subscriptionAttempt bool
}

func (f planBindingFacts) context() context.Context {
	ctx := routerKeyedCtx()
	if f.includedLane {
		ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionStateModelsEnabledContextKey{}, true)
	}
	if f.apiOnly {
		ctx = context.WithValue(ctx, credentials.SubscriptionAPIOnlyKey{}, true)
	}
	if f.enterpriseSeat {
		ctx = context.WithValue(enterpriseOnlyContext(ctx), credentials.ClaudeEnterpriseTurnKey{}, true)
	}
	if f.capRefused {
		ctx = context.WithValue(ctx, credentials.ManagedSubscriptionUsageContextKey{}, &credentials.ManagedSubscriptionUsage{ClaudeEnterpriseCapRefused: true})
		ctx = context.WithValue(ctx, credentials.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
			{Provider: providers.ProviderAnthropic, Plaintext: []byte("sk-ant-customer"), FundingPriority: auth.FundingPriorityPlanFirst},
		})
	}
	if f.ownPlan {
		ctx = context.WithValue(ctx, credentials.PickedModelOnOwnPlanContextKey{}, true)
	}
	if f.subscriptionAttempt {
		ctx = context.WithValue(ctx, credentials.SubscriptionOnlyAttemptKey{}, true)
	}
	return ctx
}

func (f planBindingFacts) planForbidsPaidFallback() bool {
	return f.enterpriseSeat && !f.capRefused || f.ownPlan || f.subscriptionAttempt || f.includedLane && !f.apiOnly
}

func TestPaidFallbackBindingMatchesThePlanRuleForEveryFactCombination(t *testing.T) {
	for mask := range 1 << 6 {
		bit := func(n int) bool { return mask&(1<<n) != 0 }
		facts := planBindingFacts{bit(0), bit(1), bit(2), bit(3), bit(4), bit(5)}
		binding := paidFallbackBinding(facts.context())
		assert.Equal(t, facts.planForbidsPaidFallback(), !binding.Admits(subscriptionlane.OrganizationCreditsServed), "%+v -> %s", facts, binding)
		assert.Equal(t, facts.planForbidsPaidFallback(), !binding.Admits(subscriptionlane.CustomerKeyServed), "%+v -> %s", facts, binding)
		assert.True(t, binding.Admits(subscriptionlane.SubscriptionServed), "%+v -> %s", facts, binding)
	}
}

func TestPaidFallbackBindingNamesWhyPaidCapacityIsClosed(t *testing.T) {
	cases := map[subscriptionlane.Binding]planBindingFacts{
		subscriptionlane.Unbound:               {},
		subscriptionlane.IncludedLane:          {includedLane: true},
		subscriptionlane.ExhaustedSet:          {includedLane: true, apiOnly: true},
		subscriptionlane.SubscriptionAttempt:   {subscriptionAttempt: true},
		subscriptionlane.OwnPlanPick:           {ownPlan: true},
		subscriptionlane.EnterpriseSeat:        {enterpriseSeat: true},
		subscriptionlane.EnterpriseCapHandover: {enterpriseSeat: true, capRefused: true},
	}
	for want, facts := range cases {
		assert.Equal(t, want, paidFallbackBinding(facts.context()), "%+v", facts)
	}
}

func TestServingEventFollowsTheCommittedCredential(t *testing.T) {
	cases := map[subscriptionlane.Event][]*requestcontext.Credentials{
		subscriptionlane.SubscriptionServed: {
			{APIKey: []byte("oauth-token"), OAuth: true, Source: requestcontext.SourceSubscription},
			{APIKey: []byte("codex-token"), OAuth: true, Source: requestcontext.SourceCodexSubscription},
		},
		subscriptionlane.CustomerKeyServed: {
			{APIKey: []byte("sk-ant-customer"), Source: requestcontext.SourceBYOK},
		},
		subscriptionlane.OrganizationCreditsServed: {
			nil,
			{APIKey: []byte("sk-client"), Source: requestcontext.SourceClient},
			{Source: requestcontext.SourceBYOK},
		},
	}
	for want, credentials := range cases {
		for _, creds := range credentials {
			assert.Equal(t, want, servingEvent(creds), "%+v", creds)
		}
	}
}
