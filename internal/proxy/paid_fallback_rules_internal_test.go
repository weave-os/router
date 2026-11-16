package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/require"
)

const paidFallbackRulesGolden = "testdata/paid_fallback_rules.golden"

type paidFallbackFacts struct {
	name  string
	apply func(context.Context) context.Context
}

func paidFallbackLanes() []paidFallbackFacts {
	marked := func(reason billing.SubscriptionOnlyReason) func(context.Context) context.Context {
		return func(ctx context.Context) context.Context { return billing.WithSubscriptionOnly(ctx, reason) }
	}
	return []paidFallbackFacts{
		{"funded", func(ctx context.Context) context.Context { return ctx }},
		{"linked-first", marked(billing.SubscriptionOnlyLinkedFirst)},
		{"spend-cap", marked(billing.SubscriptionOnlySpendCapReached)},
		{"depleted", marked(billing.SubscriptionOnlyCreditsDepleted)},
		{"credits-denied", billing.WithWeaveCreditsDenied},
		{"depleted-credits-denied", func(ctx context.Context) context.Context {
			return billing.WithWeaveCreditsDenied(billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted))
		}},
	}
}

func paidFallbackBindings() []paidFallbackFacts {
	with := func(key, value any) func(context.Context) context.Context {
		return func(ctx context.Context) context.Context { return context.WithValue(ctx, key, value) }
	}
	stateModels := func(ctx context.Context) context.Context {
		ctx = context.WithValue(ctx, InstallationSubscriptionStateModelsEnabledContextKey{}, true)
		return context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{statePaidModel})
	}
	enterpriseSeat := func(ctx context.Context) context.Context {
		return context.WithValue(enterpriseOnlyContext(ctx), claudeEnterpriseTurnKey{}, true)
	}
	return []paidFallbackFacts{
		{"open", func(ctx context.Context) context.Context { return ctx }},
		{"api-only", with(subscriptionAPIOnlyKey{}, true)},
		{"subscription-attempt", with(subscriptionOnlyAttemptKey{}, true)},
		{"own-plan-pick", with(pickedModelOnOwnPlanContextKey{}, true)},
		{"included-lane", stateModels},
		{"exhausted-set", func(ctx context.Context) context.Context {
			return context.WithValue(stateModels(ctx), subscriptionAPIOnlyKey{}, true)
		}},
		{"included-lane-attempt", func(ctx context.Context) context.Context {
			return context.WithValue(stateModels(ctx), subscriptionOnlyAttemptKey{}, true)
		}},
		{"enterprise-seat", enterpriseSeat},
		{"enterprise-cap-refused", func(ctx context.Context) context.Context {
			return context.WithValue(enterpriseSeat(ctx), ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{ClaudeEnterpriseCapRefused: true})
		}},
		{"enterprise-cap-refused-attempt", func(ctx context.Context) context.Context {
			ctx = context.WithValue(enterpriseSeat(ctx), ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{ClaudeEnterpriseCapRefused: true})
			return context.WithValue(ctx, subscriptionOnlyAttemptKey{}, true)
		}},
	}
}

func paidFallbackCustomerKeys() []paidFallbackFacts {
	row := func(priority auth.FundingPriority) func(context.Context) context.Context {
		return func(ctx context.Context) context.Context {
			return context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
				{Provider: providers.ProviderAnthropic, Plaintext: []byte("sk-ant-customer"), FundingPriority: priority},
			})
		}
	}
	return []paidFallbackFacts{
		{"none", func(ctx context.Context) context.Context { return ctx }},
		{"anthropic-plan-first", row(auth.FundingPriorityPlanFirst)},
		{"anthropic-key-first", row(auth.FundingPriorityKeyFirst)},
	}
}

func paidFallbackServingCredentials() []struct {
	name  string
	creds *Credentials
} {
	return []struct {
		name  string
		creds *Credentials
	}{
		{"deployment", nil},
		{"subscription", &Credentials{APIKey: []byte("oauth-token"), OAuth: true, Source: credSourceSubscription}},
		{"customer-key", &Credentials{APIKey: []byte("sk-ant-customer"), Source: credSourceBYOK}},
		{"broken-customer-key", &Credentials{APIKey: []byte("sk-ant-customer"), Source: credSourceBYOK, CredentialError: errors.New("unusable")}},
		{"client-key", &Credentials{APIKey: []byte("sk-client"), Source: credSourceClient}},
	}
}

func paidFallbackBit(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func paidFallbackHandover(ctx context.Context, provider string) string {
	handed := withPaidFallbackCredentials(ctx, provider)
	creds := CredentialsFromContext(handed)
	switch {
	case creds == nil:
		return "deployment"
	case creds.Source == credSourceBYOK && customerKeyFunded(creds):
		return "customer-key"
	default:
		return string(creds.Source)
	}
}

func paidFallbackRulesRow(ctx context.Context) string {
	fields := []string{
		"forbidden=" + paidFallbackBit(paidFallbackForbidden(ctx)),
		"by-plan=" + paidFallbackBit(paidFallbackForbiddenByPlan(ctx)),
		"for-anthropic=" + paidFallbackBit(paidFallbackForbiddenFor(ctx, providers.ProviderAnthropic)),
		"for-openai=" + paidFallbackBit(paidFallbackForbiddenFor(ctx, providers.ProviderOpenAI)),
		"for-claude-model=" + paidFallbackBit(paidFallbackForbiddenForModel(ctx, stateClaudeModel)),
		"for-exhausted-model=" + paidFallbackBit(paidFallbackForbiddenForModel(ctx, statePaidModel)),
		"cap-rescue=" + paidFallbackBit(claudeEnterpriseCapRescue(ctx)),
		"fallback-credential-anthropic=" + paidFallbackHandover(ctx, providers.ProviderAnthropic),
		"fallback-credential-openai=" + paidFallbackHandover(ctx, providers.ProviderOpenAI),
	}
	attempts := make([]string, 0, len(paidFallbackServingCredentials()))
	unservable := make([]string, 0, len(paidFallbackServingCredentials()))
	for _, serving := range paidFallbackServingCredentials() {
		served := context.WithValue(ctx, CredentialsContextKey{}, serving.creds)
		attempts = append(attempts, serving.name+":"+paidFallbackBit(paidAttemptForbidden(ctx, serving.creds)))
		unservable = append(unservable, serving.name+":"+paidFallbackBit(subscriptionOnlyUnservable(served, providers.ProviderAnthropic, stateClaudeModel)))
	}
	fields = append(fields, "attempt-forbidden=["+strings.Join(attempts, " ")+"]", "unservable=["+strings.Join(unservable, " ")+"]")
	return strings.Join(fields, " ")
}

func paidFallbackRulesMatrix() string {
	var out strings.Builder
	for _, lane := range paidFallbackLanes() {
		for _, binding := range paidFallbackBindings() {
			for _, key := range paidFallbackCustomerKeys() {
				ctx := key.apply(binding.apply(lane.apply(routerKeyedCtx())))
				fmt.Fprintf(&out, "lane=%s binding=%s customer-key=%s\n  %s\n", lane.name, binding.name, key.name, paidFallbackRulesRow(ctx))
			}
		}
	}
	return out.String()
}

func TestPaidFallbackRulesMatrixIsPinned(t *testing.T) {
	got := paidFallbackRulesMatrix()
	if os.Getenv("UPDATE_PAID_FALLBACK_RULES") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(paidFallbackRulesGolden), 0o750))
		require.NoError(t, os.WriteFile(paidFallbackRulesGolden, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(paidFallbackRulesGolden)
	require.NoError(t, err, "run with UPDATE_PAID_FALLBACK_RULES=1 to write %s", paidFallbackRulesGolden)
	require.Equal(t, string(want), got, "a paid-fallback rule changed for some lane x binding x customer-key x credential combination")
}
