package proxy

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

func TestPreDispatchFundingRefusalIsClassifiedAndOwned(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	svc := newServiceWithProviders(t, nil).WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
	svc.clock = func() time.Time { return now }
	disabledSeatCtx := WithManagedSubscriptionCandidates(managedSubscriptionTestContext(), []*auth.SubscriptionAccount{
		{ID: "disabled", Provider: auth.SubscriptionProviderClaude, Enabled: false, State: auth.SubscriptionAccountStateDisabled},
	})
	_, _, managed, disabledSeatErr := svc.leaseManagedSubscription(disabledSeatCtx, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String())
	require.True(t, managed)
	require.ErrorIs(t, disabledSeatErr, ErrSubscriptionPoolExhausted)

	fundingRefused := TurnErrorClass("funding_refused")
	for _, scenario := range []struct {
		name  string
		err   error
		class TurnErrorClass
		owner FailureOwner
	}{
		{name: "only linked seat disabled", err: disabledSeatErr, class: fundingRefused, owner: FailureOwnerClient},
		{name: "caller plan rate-limited behind the funding authorization", err: fmt.Errorf("%w: %w", dispatch.ErrAuthorizationDenied, errCallerPlanThrottled), class: TurnErrorRateLimited, owner: FailureOwnerClient},
		{name: "every usable linked seat throttled", err: fmt.Errorf("%w: %w", ErrSubscriptionPoolExhausted, errLinkedSeatsThrottled), class: TurnErrorRateLimited, owner: FailureOwnerProvider},
		{name: "subscription pool throttled", err: ErrSubscriptionPoolThrottled, class: TurnErrorRateLimited, owner: FailureOwnerProvider},
		{name: "credits exhausted and subscription unavailable", err: ErrCreditsExhaustedSubscriptionUnavailable, class: fundingRefused, owner: FailureOwnerClient},
		{name: "spend cap reached and subscription unavailable", err: ErrSpendCapSubscriptionUnavailable, class: fundingRefused, owner: FailureOwnerClient},
		{name: "Weave credits disabled", err: ErrWeaveCreditsDisabled, class: fundingRefused, owner: FailureOwnerClient},
		{name: "Claude Enterprise-only with no usable account", err: ErrClaudeEnterpriseSubscriptionUnavailable, class: fundingRefused, owner: FailureOwnerClient},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			assert.Equal(t, scenario.class, classifyTurnError(scenario.err, "", 0))
			assert.Equal(t, scenario.owner, classifyFailureOwner(failureOwnerInput{Err: scenario.err}))
		})
	}
}
