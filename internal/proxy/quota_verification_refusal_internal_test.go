package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
)

func TestQuotaVerificationControlsRefusal(t *testing.T) {
	unverified := &providers.UpstreamErrorResponse{
		Status: http.StatusServiceUnavailable,
		Body:   []byte(`{"error":{"type":"api_error","message":"Included Codex quota could not be verified."}}`),
		Cause:  &providers.QuotaVerificationUnavailableError{Failure: providers.QuotaVerificationFailureTimeout, UpstreamStatus: http.StatusGatewayTimeout},
	}
	exhausted := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"usage limit reached"}}`)}
	overloaded := &providers.UpstreamErrorResponse{Status: http.StatusBadGateway, Body: []byte(`{"error":{"message":"bad gateway"}}`)}
	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
	capCtx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlySpendCapReached)

	for _, tc := range []struct {
		name     string
		err      error
		controls bool
		kind     DispatchErrorKind
		status   int
	}{
		{name: "credits refusal after unverified quota", err: errors.Join(subscriptionOnlyUnavailable(ctx), unverified), controls: true},
		{name: "spend cap refusal after unverified quota", err: errors.Join(subscriptionOnlyUnavailable(capCtx), unverified), controls: true},
		{name: "pool exhaustion after unverified quota", err: errors.Join(subscriptionPoolExhausted(ctx), &providers.UpstreamStatusError{Status: http.StatusServiceUnavailable, Cause: unverified}), controls: true},
		{name: "allowance window after unverified quota", err: errors.Join(&IncludedAllowanceSpentError{orgGate: ErrCreditsExhaustedSubscriptionUnavailable}, unverified), controls: true},
		{name: "wrapped", err: fmt.Errorf("dispatch: %w", errors.Join(subscriptionOnlyUnavailable(ctx), unverified)), controls: true},
		{name: "plain credits refusal", err: subscriptionOnlyUnavailable(ctx), kind: DispatchErrorCreditsExhausted, status: http.StatusPaymentRequired},
		{name: "plain spend cap refusal", err: subscriptionOnlyUnavailable(capCtx), kind: DispatchErrorSpendCapReached, status: http.StatusPaymentRequired},
		{name: "later account exhausted", err: errors.Join(subscriptionOnlyUnavailable(ctx), unverified, exhausted), kind: DispatchErrorCreditsExhausted, status: http.StatusPaymentRequired},
		{name: "later account overloaded", err: errors.Join(subscriptionOnlyUnavailable(ctx), unverified, overloaded), kind: DispatchErrorCreditsExhausted, status: http.StatusPaymentRequired},
		{name: "caller cancelled", err: errors.Join(context.Canceled, unverified)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.controls, quotaVerificationControlsRefusal(tc.err))
			cls, ok := ClassifyDispatchError(tc.err)
			if tc.controls {
				require.True(t, ok)
				require.Equal(t, quotaVerificationUnavailableClass, cls)
				require.Equal(t, quotaVerificationUnavailableMessage, TerminalErrorMessage(tc.err))
				return
			}
			if tc.kind != 0 {
				require.True(t, ok)
				require.Equal(t, tc.kind, cls.Kind)
				require.Equal(t, tc.status, cls.Status)
			}
			require.NotEqual(t, DispatchErrorQuotaVerificationUnavailable, cls.Kind)
		})
	}
}
