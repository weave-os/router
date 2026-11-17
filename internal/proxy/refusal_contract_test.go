package proxy_test

import (
	"fmt"
	"net/http"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/eligibility"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRefusalContract_Dispatch pins every dispatch-time refusal: the kind the
// handlers key their envelope on, the status, whether it is reported as a
// client error, and the retry headers the handlers set from it.
func TestRefusalContract_Dispatch(t *testing.T) {
	for _, scenario := range []struct {
		err           error
		kind          proxy.DispatchErrorKind
		status        int
		retryAfter    bool
		noClientRetry bool
		clientError   bool
	}{
		{err: eligibility.ErrModelIneligible, kind: proxy.DispatchErrorProductIneligible, status: http.StatusForbidden, clientError: true},
		{err: proxy.ErrSubscriptionClientAppRefused, kind: proxy.DispatchErrorSubscriptionClientAppRefused, status: http.StatusForbidden, clientError: true},
		{err: proxy.ErrSpendCapSubscriptionUnavailable, kind: proxy.DispatchErrorSpendCapReached, status: http.StatusPaymentRequired, noClientRetry: true},
		{err: proxy.ErrCreditsExhaustedSubscriptionUnavailable, kind: proxy.DispatchErrorCreditsExhausted, status: http.StatusPaymentRequired, noClientRetry: true},
		{err: proxy.ErrSubscriptionUnavailableForTurn, kind: proxy.DispatchErrorSubscriptionUnavailableForTurn, status: http.StatusPaymentRequired, noClientRetry: true},
		{err: proxy.ErrWeaveCreditsDisabled, kind: proxy.DispatchErrorWeaveCreditsDisabled, status: http.StatusPaymentRequired, noClientRetry: true},
		{err: proxy.ErrClaudeEnterpriseSubscriptionUnavailable, kind: proxy.DispatchErrorClaudeEnterpriseOnly, status: http.StatusPaymentRequired, noClientRetry: true},
		{err: proxy.ErrSubscriptionPoolExhausted, kind: proxy.DispatchErrorSubscriptionPoolExhausted, status: http.StatusTooManyRequests, retryAfter: true},
		{err: proxy.ErrSubscriptionPoolThrottled, kind: proxy.DispatchErrorSubscriptionPoolThrottled, status: http.StatusTooManyRequests, retryAfter: true},
		{err: proxy.ErrSubscriptionPoolUnavailable, kind: proxy.DispatchErrorSubscriptionPoolUnavailable, status: http.StatusServiceUnavailable, retryAfter: true},
		{err: billing.ErrUserMonthlySpendLimitReached, kind: proxy.DispatchErrorUserSpendLimitReached, status: http.StatusPaymentRequired, noClientRetry: true},
		{err: billing.ErrSpendLimitCheckUnavailable, kind: proxy.DispatchErrorSpendLimitUnavailable, status: http.StatusServiceUnavailable, retryAfter: true},
		{err: router.ErrPolicyPinUnavailable, kind: proxy.DispatchErrorPolicyPinUnavailable, status: http.StatusServiceUnavailable, retryAfter: true},
	} {
		t.Run(scenario.err.Error(), func(t *testing.T) {
			class, ok := proxy.ClassifyDispatchError(fmt.Errorf("dispatch: %w", scenario.err))
			require.True(t, ok)
			assert.Equal(t, scenario.kind, class.Kind)
			assert.Equal(t, scenario.status, class.Status)
			assert.Equal(t, scenario.retryAfter, class.RetryAfter)
			assert.Equal(t, scenario.noClientRetry, class.NoClientRetry)
			assert.Equal(t, scenario.clientError, class.Kind.IsClientError())
			assert.Empty(t, proxy.OpenAIErrorCode(class.Kind))
			assert.NotEmpty(t, class.Message)
			require.NotNil(t, class.Refusal)
			assert.Equal(t, class.Status, class.Refusal.Status)
		})
	}
}
