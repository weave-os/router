package openai

import (
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/eligibility"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRefusalContract_OpenAIType pins the OpenAI error "type" each
// dispatch refusal answers with.
func TestRefusalContract_OpenAIType(t *testing.T) {
	for _, scenario := range []struct {
		err  error
		want string
	}{
		{err: eligibility.ErrModelIneligible, want: "invalid_request_error"},
		{err: proxy.ErrSubscriptionClientAppRefused, want: "invalid_request_error"},
		{err: credentials.ErrSpendCapSubscriptionUnavailable, want: "api_error"},
		{err: credentials.ErrCreditsExhaustedSubscriptionUnavailable, want: "api_error"},
		{err: proxy.ErrWeaveCreditsDisabled, want: "api_error"},
		{err: proxy.ErrClaudeEnterpriseSubscriptionUnavailable, want: "api_error"},
		{err: credentials.ErrSubscriptionPoolExhausted, want: "api_error"},
		{err: credentials.ErrSubscriptionPoolThrottled, want: "api_error"},
		{err: credentials.ErrSubscriptionPoolUnavailable, want: "api_error"},
		{err: billing.ErrUserMonthlySpendLimitReached, want: "api_error"},
		{err: billing.ErrSpendLimitCheckUnavailable, want: "api_error"},
		{err: router.ErrPolicyPinUnavailable, want: "api_error"},
	} {
		class, ok := proxy.ClassifyDispatchError(scenario.err)
		require.True(t, ok)
		assert.Equal(t, scenario.want, openAIErrorType(class), "%v", scenario.err)
	}
}
