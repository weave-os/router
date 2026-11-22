package gemini

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

// TestRefusalContract_GeminiStatus pins the Gemini "status" string each
// dispatch refusal answers with.
func TestRefusalContract_GeminiStatus(t *testing.T) {
	for _, scenario := range []struct {
		err  error
		want string
	}{
		{err: eligibility.ErrModelIneligible, want: "PERMISSION_DENIED"},
		{err: proxy.ErrSubscriptionClientAppRefused, want: "PERMISSION_DENIED"},
		{err: credentials.ErrSpendCapSubscriptionUnavailable, want: "FAILED_PRECONDITION"},
		{err: credentials.ErrCreditsExhaustedSubscriptionUnavailable, want: "FAILED_PRECONDITION"},
		{err: credentials.ErrWeaveCreditsDisabled, want: "FAILED_PRECONDITION"},
		{err: proxy.ErrClaudeEnterpriseSubscriptionUnavailable, want: "FAILED_PRECONDITION"},
		{err: credentials.ErrSubscriptionPoolExhausted, want: "RESOURCE_EXHAUSTED"},
		{err: credentials.ErrSubscriptionPoolThrottled, want: "RESOURCE_EXHAUSTED"},
		{err: credentials.ErrSubscriptionPoolUnavailable, want: "UNAVAILABLE"},
		{err: billing.ErrUserMonthlySpendLimitReached, want: "FAILED_PRECONDITION"},
		{err: billing.ErrSpendLimitCheckUnavailable, want: "UNAVAILABLE"},
		{err: router.ErrPolicyPinUnavailable, want: "UNAVAILABLE"},
	} {
		class, ok := proxy.ClassifyDispatchError(scenario.err)
		require.True(t, ok)
		assert.Equal(t, scenario.want, geminiErrorStatus(class), "%v", scenario.err)
	}
}
