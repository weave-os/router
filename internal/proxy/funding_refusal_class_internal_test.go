package proxy

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/telemetry/turnrecord"
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
	require.ErrorIs(t, disabledSeatErr, credentials.ErrSubscriptionPoolExhausted)

	fundingRefused := turnrecord.TurnErrorFundingRefused
	for _, scenario := range []struct {
		name  string
		err   error
		class turnrecord.TurnErrorClass
		owner turnrecord.FailureOwner
	}{
		{name: "only linked seat disabled", err: disabledSeatErr, class: fundingRefused, owner: turnrecord.FailureOwnerClient},
		{name: "caller plan rate-limited behind the funding authorization", err: fmt.Errorf("%w: %w", dispatch.ErrAuthorizationDenied, credentials.ErrCallerPlanThrottled), class: turnrecord.TurnErrorRateLimited, owner: turnrecord.FailureOwnerClient},
		{name: "every usable linked seat throttled", err: fmt.Errorf("%w: %w", credentials.ErrSubscriptionPoolExhausted, errLinkedSeatsThrottled), class: turnrecord.TurnErrorRateLimited, owner: turnrecord.FailureOwnerProvider},
		{name: "subscription pool throttled", err: credentials.ErrSubscriptionPoolThrottled, class: turnrecord.TurnErrorRateLimited, owner: turnrecord.FailureOwnerProvider},
		{name: "credits exhausted and subscription unavailable", err: credentials.ErrCreditsExhaustedSubscriptionUnavailable, class: fundingRefused, owner: turnrecord.FailureOwnerClient},
		{name: "spend cap reached and subscription unavailable", err: credentials.ErrSpendCapSubscriptionUnavailable, class: fundingRefused, owner: turnrecord.FailureOwnerClient},
		{name: "Weave credits disabled", err: ErrWeaveCreditsDisabled, class: fundingRefused, owner: turnrecord.FailureOwnerClient},
		{name: "Claude Enterprise-only with no usable account", err: ErrClaudeEnterpriseSubscriptionUnavailable, class: fundingRefused, owner: turnrecord.FailureOwnerClient},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			assert.Equal(t, scenario.class, classifyTurnError(scenario.err, "", 0))
			assert.Equal(t, scenario.owner, classifyFailureOwner(failureOwnerInput{Err: scenario.err}))
		})
	}
}
