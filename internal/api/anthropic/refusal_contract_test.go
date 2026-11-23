package anthropic

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

// TestRefusalContract_AnthropicType pins the Anthropic error "type" each
// dispatch refusal answers with.
func TestRefusalContract_AnthropicType(t *testing.T) {
	for _, scenario := range []struct {
		err  error
		want string
	}{
		{err: eligibility.ErrModelIneligible, want: "permission_error"},
		{err: credentials.ErrSubscriptionClientAppRefused, want: "permission_error"},
		{err: credentials.ErrSpendCapSubscriptionUnavailable, want: "billing_error"},
		{err: credentials.ErrCreditsExhaustedSubscriptionUnavailable, want: "billing_error"},
		{err: credentials.ErrWeaveCreditsDisabled, want: "billing_error"},
		{err: credentials.ErrClaudeEnterpriseSubscriptionUnavailable, want: "billing_error"},
		{err: credentials.ErrSubscriptionPoolExhausted, want: "rate_limit_error"},
		{err: credentials.ErrSubscriptionPoolThrottled, want: "rate_limit_error"},
		{err: credentials.ErrSubscriptionPoolUnavailable, want: "api_error"},
		{err: billing.ErrUserMonthlySpendLimitReached, want: "billing_error"},
		{err: billing.ErrSpendLimitCheckUnavailable, want: "api_error"},
		{err: router.ErrPolicyPinUnavailable, want: "api_error"},
	} {
		class, ok := proxy.ClassifyDispatchError(scenario.err)
		require.True(t, ok)
		assert.Equal(t, scenario.want, anthropicErrorType(class), "%v", scenario.err)
	}
}
