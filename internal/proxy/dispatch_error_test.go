package proxy_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"workweave/router/internal/billing"
	"workweave/router/internal/providers"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router/bandit"
	"workweave/router/internal/router/cluster"
	"workweave/router/internal/router/hmm"
	"workweave/router/internal/router/policy"
	"workweave/router/internal/router/rl"
	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyDispatchError_UnknownErrorIsUnmatched(t *testing.T) {
	_, ok := proxy.ClassifyDispatchError(errors.New("boom"))
	assert.False(t, ok, "an error not matching any known sentinel must not be classified")
}

func TestClassifyDispatchError_ProviderNotConfigured(t *testing.T) {
	// This is the exact wrapping service.go's dispatch switch uses (fmt.Errorf("%w: %s", ErrProviderNotConfigured, name)).
	err := fmt.Errorf("%w: %s", proxy.ErrProviderNotConfigured, "some-provider")

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok, "ErrProviderNotConfigured must be classified")
	assert.Equal(t, proxy.DispatchErrorProviderNotConfigured, cls.Kind)
	assert.Equal(t, http.StatusBadGateway, cls.Status)
	assert.Equal(t, "Provider not configured.", cls.Message)
	assert.False(t, cls.RetryAfter)
	assert.False(t, cls.Kind.IsClientError(), "provider-not-configured is an upstream/routing problem, not a client-input one")
}

func TestClassifyDispatchError_UpstreamStatusErrorPreservesStatus(t *testing.T) {
	err := &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorUpstreamStatus, cls.Kind)
	assert.Equal(t, http.StatusTooManyRequests, cls.Status)
}

func TestClassifyDispatchError_UpstreamErrorResponsePreservesStatus(t *testing.T) {
	err := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"rate limited"}}`)}

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorUpstreamStatus, cls.Kind)
	assert.Equal(t, http.StatusTooManyRequests, cls.Status)
}

func TestClassifyDispatchError_ClusterUnavailableRetriesAndLogsError(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(cluster.ErrClusterUnavailable)

	require.True(t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, cls.Status)
	assert.True(t, cls.RetryAfter)
	assert.Equal(t, "error", cls.LogLevel)
}

func TestClassifyDispatchError_NoEligibleProviderIsClientErrorAndWarns(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(cluster.ErrNoEligibleProvider)

	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
	assert.Equal(t, "warn", cls.LogLevel)
	assert.False(t, cls.RetryAfter)
}

// ErrAllowlistEmptiesPool wraps ErrNoEligibleProvider, so switch ordering is
// load-bearing: the generic case must not match first and misattribute the cause.
func TestClassifyDispatchError_AllowlistEmptiesPoolPrecedesNoEligibleProvider(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(cluster.ErrAllowlistEmptiesPool)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorAllowlistEmptiesPool, cls.Kind)
	assert.NotEqual(t, proxy.DispatchErrorNoEligibleProvider, cls.Kind, "must not fall through to the generic no-eligible-provider case")
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
	assert.Equal(t, "warn", cls.LogLevel)
	assert.False(t, cls.RetryAfter)
	assert.Contains(t, cls.Message, "allowlist")
}

func TestClassifyDispatchError_BanditRLandHMMUnavailableRetry(t *testing.T) {
	for _, err := range []error{bandit.ErrBanditUnavailable, rl.ErrPolicyUnavailable, hmm.ErrHMMUnavailable} {
		cls, ok := proxy.ClassifyDispatchError(err)
		require.True(t, ok, "expected %v to be classified", err)
		assert.Equal(t, http.StatusServiceUnavailable, cls.Status)
		assert.True(t, cls.RetryAfter)
	}
}

func TestClassifyDispatchError_CreditsExhaustedIs402(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(proxy.ErrCreditsExhaustedSubscriptionUnavailable)

	require.True(t, ok, "the credits-exhausted sentinel must be classified")
	assert.Equal(t, proxy.DispatchErrorCreditsExhausted, cls.Kind)
	assert.Equal(t, http.StatusPaymentRequired, cls.Status)
	assert.Contains(t, cls.Message, "credits are exhausted", "the client message must explain the depleted balance")
	assert.Contains(t, cls.Message, "weave-router", "the client message must surface the top-up CTA")
	assert.Equal(t, "warn", cls.LogLevel)
	assert.False(t, cls.RetryAfter, "a retry won't help until credits are added")
}

func TestClassifyDispatchError_NotImplementedDoesNotLog(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(providers.ErrNotImplemented)

	require.True(t, ok)
	assert.Equal(t, http.StatusNotImplemented, cls.Status)
	assert.Empty(t, cls.LogLevel)
}

func TestClassifyDispatchError_TranslationIntrinsicIncompatibilityIs400(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(proxy.ErrTranslationIntrinsicallyIncompatible)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorTranslationIntrinsicallyIncompatible, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
	assert.False(t, cls.RetryAfter)
}

func TestClassifyDispatchError_TranslationCompatibleProviderUnavailableIs503(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(proxy.ErrTranslationCompatibleProviderUnavailable)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorTranslationProviderUnavailable, cls.Kind)
	assert.Equal(t, http.StatusServiceUnavailable, cls.Status)
	assert.False(t, cls.Kind.IsClientError())
	assert.True(t, cls.RetryAfter)
}

func TestClassifyDispatchError_UserSpendLimitReachedIs402(t *testing.T) {
	err := fmt.Errorf("%w: spent 5 of 5 usd micros", billing.ErrUserMonthlySpendLimitReached)

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorUserSpendLimitReached, cls.Kind)
	assert.Equal(t, http.StatusPaymentRequired, cls.Status)
	assert.False(t, cls.RetryAfter)
	assert.Equal(t, "warn", cls.LogLevel)
	assert.False(t, cls.Kind.IsClientError())
}

func TestClassifyDispatchError_SpendLimitUnavailableFailsClosed503(t *testing.T) {
	err := fmt.Errorf("%w: %v", billing.ErrSpendLimitCheckUnavailable, errors.New("pg down"))

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorSpendLimitUnavailable, cls.Kind)
	assert.Equal(t, http.StatusServiceUnavailable, cls.Status)
	assert.True(t, cls.RetryAfter)
	assert.Equal(t, "error", cls.LogLevel)
}

func TestClassifyDispatchError_AnthropicCacheControlOverflowIs400(t *testing.T) {
	// Mirror the wrapping service.go uses: fmt.Errorf("emit body: %w", emitErr).
	err := fmt.Errorf("emit body: %w", fmt.Errorf("%w: got 5, maximum is 4", translate.ErrAnthropicCacheControlOverflow))

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok, "ErrAnthropicCacheControlOverflow must be classified, not fall through to a generic 502")
	assert.Equal(t, proxy.DispatchErrorAnthropicCacheControlInvalid, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError(), "the client's own explicit breakpoints exceeded capacity, not an upstream/routing problem")
	assert.Equal(t, "warn", cls.LogLevel)
	assert.False(t, cls.RetryAfter)
	assert.NotContains(t, cls.Message, "emit body:", "the internal wrap-chain prefix must not leak into the client-facing message")
	assert.Contains(t, cls.Message, "got 5, maximum is 4", "the validator's dynamic detail must survive unwrapping")
}

func TestClassifyDispatchError_AnthropicCacheControlInvalidTTLOrderingIs400(t *testing.T) {
	err := fmt.Errorf("emit body: %w", fmt.Errorf("%w: ttl=1h cache_control must not follow ttl=5m", translate.ErrAnthropicCacheControlInvalid))

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorAnthropicCacheControlInvalid, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
	assert.NotContains(t, cls.Message, "emit body:", "the internal wrap-chain prefix must not leak into the client-facing message")
	assert.Contains(t, cls.Message, "ttl=1h cache_control must not follow ttl=5m")
}

func TestClassifyDispatchError_ForcedModelUnknownIs400(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(&proxy.ForcedModelUnknownError{Model: "gpt-"})

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorForcedModelUnknown, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError(), "an unresolvable force is a client-input problem")
	assert.Contains(t, cls.Message, "gpt-", "the caller must see the value that failed to resolve")
	assert.Equal(t, "warn", cls.LogLevel)
}

func TestClassifyDispatchError_ForcedClusterUnsupportedStrategyIs400(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(&proxy.ForcedClusterUnsupportedStrategyError{
		Cluster:  "maximum",
		Strategy: "cluster",
	})

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorForcedClusterUnsupportedStrategy, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError())
	assert.Contains(t, cls.Message, "maximum")
	assert.Contains(t, cls.Message, proxy.ForceClusterHeader, "the caller must be told which header to clear")
}

// The unservable error is raised inside the policy router, so it must classify
// through the wrap chain Route returns it in — not just bare.
func TestClassifyDispatchError_ForcedClusterUnservableIs400(t *testing.T) {
	err := fmt.Errorf("route: %w", &policy.ForcedClusterUnservableError{
		Cluster: "explore",
		Reason:  `"explore" is not a routing cluster on this installation`,
	})

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorForcedClusterUnservable, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.True(t, cls.Kind.IsClientError(), "a bad cluster label is a client error, not a sidecar outage")
	assert.Contains(t, cls.Message, "explore")
	assert.NotContains(t, cls.Message, "route:", "the internal wrap prefix must not leak to the client")
}

// Build-time intrinsic-incompatibility must classify as a clear 502.
func TestClassifyDispatchError_RoutedModelIncompatibleIs502(t *testing.T) {
	err := fmt.Errorf("translate anthropic request to gemini: %w", translate.ErrGeminiUnsignedToolHistory)

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorRoutedModelIncompatible, cls.Kind)
	assert.Equal(t, http.StatusBadGateway, cls.Status)
	assert.False(t, cls.Kind.IsClientError(), "the routed model was incompatible, not the client's request")
	assert.Equal(t, "warn", cls.LogLevel)
}

func TestClassifyDispatchError_ReasoningIncompatibleClassifiesAsRoutedModelIncompatible(t *testing.T) {
	err := fmt.Errorf("emit body: %w", translate.ErrReasoningIncompatible)

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorRoutedModelIncompatible, cls.Kind)
	assert.Equal(t, http.StatusBadGateway, cls.Status)
}

// TestClassifyDispatchError_GatewayServesNoModel: sidecar wraps both sentinels;
// the specific one must win over the generic "router unavailable" shape.
func TestClassifyDispatchError_GatewayServesNoModel(t *testing.T) {
	err := fmt.Errorf("hmm_embedding: no eligible candidate: %w: %w",
		policy.ErrGatewayServesNoDeployedModel, hmm.ErrHMMUnavailable)

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorGatewayServesNoModel, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.Contains(t, cls.Message, "model aliases")
	assert.False(t, cls.RetryAfter, "retrying cannot fix a configuration that routes nowhere")
	assert.True(t, cls.Kind.IsClientError())
}

func TestClassifyDispatchError_NoRoutableModels(t *testing.T) {
	err := fmt.Errorf("hmm_embedding: no eligible candidate: %w: %w",
		policy.ErrNoRoutableModels, hmm.ErrHMMUnavailable)

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorNoRoutableModels, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.False(t, cls.RetryAfter)
}

func TestClassifyDispatchError_HMMUnavailableStaysAnOutage(t *testing.T) {
	cls, ok := proxy.ClassifyDispatchError(fmt.Errorf("hmm: sidecar decide: %w", hmm.ErrHMMUnavailable))

	require.True(t, ok)
	assert.Equal(t, proxy.DispatchErrorHMMUnavailable, cls.Kind)
	assert.Equal(t, http.StatusServiceUnavailable, cls.Status)
}

func TestClassifyDispatchError_ResponsesChatCompletionsBodyIs400ClientError(t *testing.T) {
	// ProxyOpenAIResponses' exact wrapping of the ingress rejection.
	err := fmt.Errorf("translate responses request: %w", translate.ErrResponsesChatCompletionsBody)

	cls, ok := proxy.ClassifyDispatchError(err)

	require.True(t, ok, "a Chat Completions body on the Responses surface must be classified")
	assert.Equal(t, proxy.DispatchErrorResponsesChatCompletionsBody, cls.Kind)
	assert.Equal(t, http.StatusBadRequest, cls.Status)
	assert.Contains(t, cls.Message, "moved to 'input'")
	assert.True(t, cls.Kind.IsClientError(), "the caller sent the wrong wire format; not an upstream failure")
}
