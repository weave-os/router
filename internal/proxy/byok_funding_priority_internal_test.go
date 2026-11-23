package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fundingPriorityCtx(ctx context.Context, provider string, priority auth.FundingPriority) context.Context {
	return context.WithValue(ctx, credentials.ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: provider, FundingPriority: priority, Plaintext: []byte("synthetic-byok-" + provider)},
	})
}

func TestPlanFirstAnthropicKeyLeavesInboundSubscriptionFirst(t *testing.T) {
	ctx := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderAnthropic, auth.FundingPriorityPlanFirst)
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-enterprise-seat"}}

	creds := credentials.CredentialsFromContext(credentials.ResolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers))

	require.NotNil(t, creds)
	assert.True(t, creds.OAuth, "a plan-first key must not displace the caller's Claude plan")
	assert.Equal(t, requestcontext.SourceSubscription, creds.Source)
	assert.False(t, credentials.AnthropicBYOKFirst(ctx))
}

func TestPlanFirstAnthropicKeyFundsTurnsWithoutClaudePlan(t *testing.T) {
	// A Codex harness routed to Claude carries no Claude credential.
	ctx := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderAnthropic, auth.FundingPriorityPlanFirst)
	headers := http.Header{"Authorization": []string{"Bearer " + codexTestJWT}, "Chatgpt-Account-Id": []string{"acct-1"}}

	creds := credentials.CredentialsFromContext(credentials.ResolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers))

	require.NotNil(t, creds)
	assert.Equal(t, requestcontext.SourceBYOK, creds.Source)
	assert.Equal(t, []byte("synthetic-byok-anthropic"), creds.APIKey)
}

func TestPlanFirstAnthropicKeyBacksSuppressedClaudePlan(t *testing.T) {
	ctx := credentials.WithSuppressedClaudeSubscription(fundingPriorityCtx(routerKeyedCtx(), providers.ProviderAnthropic, auth.FundingPriorityPlanFirst))
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-spent"}}

	creds := credentials.CredentialsFromContext(credentials.ResolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers))

	require.NotNil(t, creds, "a suppressed plan must fall back to the customer's key, not the deployment key")
	assert.Equal(t, requestcontext.SourceBYOK, creds.Source)
}

func TestKeyFirstAnthropicKeyBeatsInboundSubscription(t *testing.T) {
	ctx := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderAnthropic, auth.FundingPriorityKeyFirst)
	headers := http.Header{"Authorization": []string{"Bearer sk-ant-oat01-enterprise-seat"}}

	creds := credentials.CredentialsFromContext(credentials.ResolveAndInjectCredentials(ctx, providers.ProviderAnthropic, "claude-opus-4-8", headers))

	require.NotNil(t, creds)
	assert.False(t, creds.OAuth)
	assert.Equal(t, requestcontext.SourceBYOK, creds.Source)
	assert.True(t, credentials.AnthropicBYOKFirst(ctx))
}

func TestOpenAIKeyFundingPriority(t *testing.T) {
	for _, tc := range []struct {
		priority auth.FundingPriority
		source   string
	}{
		{auth.FundingPriorityPlanFirst, requestcontext.SourceCodexSubscription},
		{"", requestcontext.SourceCodexSubscription},
		{auth.FundingPriorityKeyFirst, requestcontext.SourceBYOK},
	} {
		t.Run(string(tc.priority), func(t *testing.T) {
			ctx := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderOpenAI, tc.priority)
			ctx = context.WithValue(ctx, credentials.OpenAISubscriptionContextKey{}, codexTestJWT)
			ctx = context.WithValue(ctx, credentials.OpenAIAccountIDContextKey{}, "acct-1")

			creds := credentials.CredentialsFromContext(credentials.ResolveAndInjectCredentials(ctx, providers.ProviderOpenAI, "gpt-5.6-sol", http.Header{}))

			require.NotNil(t, creds)
			assert.Equal(t, tc.source, creds.Source)
		})
	}
}

func TestPlanFirstAnthropicKeyKeepsEnterprisePlanInPlay(t *testing.T) {
	ctx := context.WithValue(routerKeyedCtx(), credentials.AnthropicSubscriptionContextKey{}, parityAnthropicToken)
	planFirst := enterpriseOnlyContext(fundingPriorityCtx(ctx, providers.ProviderAnthropic, auth.FundingPriorityPlanFirst))
	keyFirst := enterpriseOnlyContext(fundingPriorityCtx(ctx, providers.ProviderAnthropic, auth.FundingPriorityKeyFirst))

	assert.True(t, credentials.ClaudeEnterpriseOnly(planFirst))
	assert.False(t, credentials.ClaudeEnterpriseOnly(keyFirst))
}

func TestKeyFirstOpenAIKeyHidesCodexPlan(t *testing.T) {
	headers := http.Header{
		"Authorization":      []string{"Bearer " + codexTestJWT},
		"Chatgpt-Account-Id": []string{"acct-1"},
	}
	keyFirst := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderOpenAI, auth.FundingPriorityKeyFirst)
	keyFirst = context.WithValue(keyFirst, credentials.OpenAISubscriptionContextKey{}, codexTestJWT)

	codex, _ := credentials.PresentSubscriptionTokens(keyFirst, headers)
	assert.Empty(t, codex, "a key-first OpenAI key must not enter Codex subscription bypass")
	assert.False(t, codexResponsesRequest(keyFirst, headers))

	planFirst := fundingPriorityCtx(routerKeyedCtx(), providers.ProviderOpenAI, auth.FundingPriorityPlanFirst)
	codex, _ = credentials.PresentSubscriptionTokens(planFirst, headers)
	assert.NotEmpty(t, codex)
	assert.True(t, codexResponsesRequest(planFirst, headers))
}
