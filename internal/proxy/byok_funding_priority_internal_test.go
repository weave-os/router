package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fundingPriorityCtx(ctx context.Context, provider string, priority auth.FundingPriority) context.Context {
	return context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: provider, FundingPriority: priority, Plaintext: []byte("synthetic-byok-" + provider)},
	})
}

func TestPlanFirstAnthropicKeyLeavesInboundSubscriptionFirst(t *testing.T) {
	ctx := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderAnthropic, auth.FundingPriorityPlanFirst)
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-enterprise-seat"}}

	creds := CredentialsFromContext(resolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers))

	require.NotNil(t, creds)
	assert.True(t, creds.OAuth, "a plan-first key must not displace the caller's Claude plan")
	assert.Equal(t, credSourceSubscription, creds.Source)
	assert.False(t, anthropicBYOKFirst(ctx))
}

func TestPlanFirstAnthropicKeyFundsTurnsWithoutClaudePlan(t *testing.T) {
	// A Codex harness routed to Claude carries no Claude credential.
	ctx := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderAnthropic, auth.FundingPriorityPlanFirst)
	headers := http.Header{"Authorization": []string{"Bearer " + codexTestJWT}, "Chatgpt-Account-Id": []string{"acct-1"}}

	creds := CredentialsFromContext(resolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers))

	require.NotNil(t, creds)
	assert.Equal(t, credSourceBYOK, creds.Source)
	assert.Equal(t, []byte("synthetic-byok-anthropic"), creds.APIKey)
}

func TestPlanFirstAnthropicKeyBacksSuppressedClaudePlan(t *testing.T) {
	ctx := withSuppressedClaudeSubscription(fundingPriorityCtx(routerKeyedCtx(), providers.ProviderAnthropic, auth.FundingPriorityPlanFirst))
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-spent"}}

	creds := CredentialsFromContext(resolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers))

	require.NotNil(t, creds, "a suppressed plan must fall back to the customer's key, not the deployment key")
	assert.Equal(t, credSourceBYOK, creds.Source)
}

func TestKeyFirstAnthropicKeyBeatsInboundSubscription(t *testing.T) {
	ctx := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderAnthropic, auth.FundingPriorityKeyFirst)
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-enterprise-seat"}}

	creds := CredentialsFromContext(resolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers))

	require.NotNil(t, creds)
	assert.False(t, creds.OAuth)
	assert.Equal(t, credSourceBYOK, creds.Source)
	assert.True(t, anthropicBYOKFirst(ctx))
}

func TestOpenAIKeyFundingPriority(t *testing.T) {
	for _, tc := range []struct {
		priority auth.FundingPriority
		source   string
	}{
		{auth.FundingPriorityPlanFirst, credSourceCodexSubscription},
		{"", credSourceCodexSubscription},
		{auth.FundingPriorityKeyFirst, credSourceBYOK},
	} {
		t.Run(string(tc.priority), func(t *testing.T) {
			ctx := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderOpenAI, tc.priority)
			ctx = context.WithValue(ctx, OpenAISubscriptionContextKey{}, codexTestJWT)
			ctx = context.WithValue(ctx, OpenAIAccountIDContextKey{}, "acct-1")

			creds := CredentialsFromContext(resolveAndInjectCredentials(ctx, providers.ProviderOpenAI, "gpt-5.6-sol", http.Header{}))

			require.NotNil(t, creds)
			assert.Equal(t, tc.source, creds.Source)
		})
	}
}

func TestPlanFirstAnthropicKeyKeepsEnterprisePlanInPlay(t *testing.T) {
	ctx := context.WithValue(routerKeyedCtx(), AnthropicSubscriptionContextKey{}, parityAnthropicToken)
	planFirst := enterpriseOnlyContext(fundingPriorityCtx(ctx, providers.ProviderAnthropic, auth.FundingPriorityPlanFirst))
	keyFirst := enterpriseOnlyContext(fundingPriorityCtx(ctx, providers.ProviderAnthropic, auth.FundingPriorityKeyFirst))

	assert.True(t, claudeEnterpriseOnly(planFirst))
	assert.False(t, claudeEnterpriseOnly(keyFirst))
}
