package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

func enterpriseCapRefusalHeaders() http.Header {
	h := make(http.Header)
	h.Set("anthropic-ratelimit-unified-status", "rejected")
	h.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	h.Set("anthropic-ratelimit-unified-representative-claim", "overage")
	h.Set("anthropic-ratelimit-unified-overage-in-use", "true")
	return h
}

func enterpriseCapUpstream(ingress parityIngress, headers http.Header) *parityUpstream {
	return &parityUpstream{
		subErr: &providers.UpstreamErrorResponse{
			Status: http.StatusTooManyRequests, Headers: headers,
			Body: []byte(`{"error":{"type":"rate_limit_error"}}`),
		},
		subHeaders: headers,
		okBody:     ingress.upstreamOK(false),
	}
}

func withAnthropicKey(ctx context.Context, priority auth.FundingPriority) context.Context {
	return context.WithValue(ctx, ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderAnthropic, Plaintext: []byte(parityAnthropicBYOK), FundingPriority: priority},
	})
}

func TestClaudeEnterpriseOverageCapRefusalFallsBackToPlanFirstKey(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := enterpriseCapUpstream(ingress, enterpriseCapRefusalHeaders())
	service := ingress.quotaService(upstream)
	ctx := withAnthropicKey(enterpriseOnlyContext(ingress.subCtx()), auth.FundingPriorityPlanFirst)
	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, ctx, body, recorder, request))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Positive(t, upstream.subDispatches)
	require.Equal(t, []string{parityAnthropicBYOK}, upstream.paidKeys, "only the customer's key may fund the turn after the cap")
}

func TestClaudeEnterpriseOverageCapRefusalNeverSpendsWeaveCredits(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := enterpriseCapUpstream(ingress, enterpriseCapRefusalHeaders())
	service := ingress.quotaService(upstream)
	recorder, request, body := ingress.request(t, false)
	require.Error(t, ingress.call(service, enterpriseOnlyContext(ingress.subCtx()), body, recorder, request))
	require.Positive(t, upstream.subDispatches)
	require.Empty(t, upstream.paidKeys)
}

func TestClaudeEnterpriseRateLimitWithoutCapRefusalKeepsPlanFirstKeyIdle(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := enterpriseCapUpstream(ingress, http.Header{})
	service := ingress.quotaService(upstream)
	ctx := withAnthropicKey(enterpriseOnlyContext(ingress.subCtx()), auth.FundingPriorityPlanFirst)
	recorder, request, body := ingress.request(t, false)
	require.Error(t, ingress.call(service, ctx, body, recorder, request))
	require.Positive(t, upstream.subDispatches)
	require.Empty(t, upstream.paidKeys)
}

func TestClaudeEnterpriseOverageCapKeyFailureDoesNotReachWeaveCredits(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := enterpriseCapUpstream(ingress, enterpriseCapRefusalHeaders())
	upstream.paidErr = upstreamErr(http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error"}}`)
	service := ingress.quotaService(upstream)
	ctx := withAnthropicKey(enterpriseOnlyContext(ingress.subCtx()), auth.FundingPriorityPlanFirst)
	recorder, request, body := ingress.request(t, false)
	require.Error(t, ingress.call(service, ctx, body, recorder, request))
	require.NotEmpty(t, upstream.paidKeys)
	for _, key := range upstream.paidKeys {
		require.Equal(t, parityAnthropicBYOK, key, "a failing customer key must not fall through to the deployment key")
	}
}

func TestClaudeEnterpriseOverageCapRescueSurvivesEmptyLinkedPool(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := enterpriseCapUpstream(ingress, enterpriseCapRefusalHeaders())
	service := ingress.quotaService(upstream).WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
	ctx := withAnthropicKey(enterpriseOnlyContext(ingress.subCtx()), auth.FundingPriorityPlanFirst)
	ctx = context.WithValue(ctx, ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{auth.SubscriptionProviderClaude: {}})
	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, ctx, body, recorder, request))
	require.Equal(t, []string{parityAnthropicBYOK}, upstream.paidKeys)
}

func TestClaudeEnterpriseOverageCapBypassReroutesToPlanFirstKey(t *testing.T) {
	ingress := parityAnthropicIngress()
	upstream := enterpriseCapUpstream(ingress, enterpriseCapRefusalHeaders())
	service := ingress.quotaService(upstream)
	threshold := 0.9
	ctx := withAnthropicKey(enterpriseOnlyContext(ingress.subCtx()), auth.FundingPriorityPlanFirst)
	ctx = context.WithValue(ctx, InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true, Threshold: &threshold})
	recorder, request, body := ingress.request(t, false)
	require.NoError(t, ingress.call(service, ctx, body, recorder, request))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []string{parityAnthropicBYOK}, upstream.paidKeys)
}
