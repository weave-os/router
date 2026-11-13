package billing_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSubscriberPrepaid struct {
	mu          sync.Mutex
	balance     int64
	settlements []billing.PrepaidSettlement
	settleErr   error
}

func (f *fakeSubscriberPrepaid) Balance(context.Context, billing.Owner) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balance, nil
}

func (f *fakeSubscriberPrepaid) Debit(context.Context, billing.PrepaidDebit) (int64, error) {
	panic("subscriber authorization tests must not use the legacy debit")
}

func (f *fakeSubscriberPrepaid) Authorize(_ context.Context, request billing.PrepaidAuthorizationRequest) (billing.PrepaidAuthorization, error) {
	return billing.PrepaidAuthorization{
		Owner:             request.Owner,
		ActionID:          request.ActionID,
		RouterRequestID:   request.RouterRequestID,
		APIKeyID:          request.APIKeyID,
		RequestedModel:    request.RequestedModel,
		ReservedUsdMicros: request.UpperBoundUsdMicros,
		State:             billing.PrepaidAuthorizationReserved,
		CapacitySource:    entitlement.CapacitySourcePrepaid,
	}, nil
}

func (f *fakeSubscriberPrepaid) Settle(_ context.Context, settlement billing.PrepaidSettlement) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settlements = append(f.settlements, settlement)
	if f.settleErr != nil {
		return f.balance, f.settleErr
	}
	f.balance -= settlement.RetailUsdMicros
	return f.balance, nil
}

func (f *fakeSubscriberPrepaid) Finalize(context.Context, string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balance, nil
}

func prepaidContext() context.Context {
	return billing.WithPrepaidAuthorization(context.Background(), billing.PrepaidAuthorization{
		Owner:             billing.SubscriberOwner("11111111-1111-1111-1111-111111111111"),
		ActionID:          "req_sub:prepaid-hold",
		RouterRequestID:   "req_sub",
		APIKeyID:          "key_1",
		RequestedModel:    entitlement.ModelUnresolved,
		ReservedUsdMicros: 10_000_000,
		State:             billing.PrepaidAuthorizationReserved,
		CapacitySource:    entitlement.CapacitySourcePrepaid,
	})
}

func TestDebitForInferenceSettlesSubscriberPrepaidExactly(t *testing.T) {
	orgRepo := &fakeRepo{balanceRowExists: true, balanceMicros: 20_000_000}
	subscriber := &fakeSubscriberPrepaid{balance: 10_000_000}
	svc := billing.NewService(orgRepo).WithSubscriberPrepaid(subscriber)

	balance, err := svc.DebitForInference(prepaidContext(), subscriberParams())
	require.NoError(t, err)
	assert.Equal(t, int64(7_000_000), balance)
	assert.Empty(t, orgRepo.ledgerCalls, "subscriber-funded turns cannot reach the organization book")
	require.Len(t, subscriber.settlements, 1)
	assert.Equal(t, int64(3_000_000), subscriber.settlements[0].RetailUsdMicros)
	assert.Equal(t, entitlement.CapacitySourcePrepaid, subscriber.settlements[0].CapacitySource)
	assert.Equal(t, "req_sub:prepaid-hold", subscriber.settlements[0].AuthorizationActionID)
}

func TestDebitForInferenceSignalsSubscriberAutopayCrossing(t *testing.T) {
	orgRepo := &fakeRepo{
		balanceRowExists: true,
		balanceMicros:    20_000_000,
		autopayEnabled:   true,
		autopayThreshold: 8_000_000,
	}
	subscriber := &fakeSubscriberPrepaid{balance: 10_000_000}
	notifier := &fakeAutopayNotifier{}
	svc := billing.NewService(orgRepo).
		WithSubscriberPrepaid(subscriber).
		WithAutopayNotifier(notifier)

	_, err := svc.DebitForInference(prepaidContext(), subscriberParams())
	require.NoError(t, err)
	assert.Equal(t, []billing.Owner{
		billing.SubscriberOwner("11111111-1111-1111-1111-111111111111"),
	}, orgRepo.autopayConfigOwners())
	assert.Equal(t, []billing.Owner{
		billing.SubscriberOwner("11111111-1111-1111-1111-111111111111"),
	}, notifier.calls())
	assert.Empty(t, orgRepo.ledgerCalls)
}

func TestDebitForInferenceDoesNotSettlePrepaidForLinkedCapacity(t *testing.T) {
	orgRepo := &fakeRepo{balanceRowExists: true, balanceMicros: 20_000_000}
	subscriber := &fakeSubscriberPrepaid{balance: 10_000_000}
	svc := billing.NewService(orgRepo).WithSubscriberPrepaid(subscriber)
	params := subscriberParams()
	params.SubscriptionServed = true

	balance, err := svc.DebitForInference(prepaidContext(), params)
	require.NoError(t, err)
	assert.Equal(t, int64(10_000_000), balance)
	assert.Empty(t, subscriber.settlements)
	assert.Empty(t, orgRepo.ledgerCalls)
}

func TestDebitForInferencePreservesPrepaidHoldOnSettlementFailure(t *testing.T) {
	orgRepo := &fakeRepo{balanceRowExists: true, balanceMicros: 20_000_000}
	subscriber := &fakeSubscriberPrepaid{balance: 10_000_000, settleErr: errors.New("settlement unavailable")}
	svc := billing.NewService(orgRepo).WithSubscriberPrepaid(subscriber)
	ctx := prepaidContext()

	_, err := svc.DebitForInference(ctx, subscriberParams())
	require.Error(t, err)
	assert.True(t, billing.PrepaidSettlementFailed(ctx))
	assert.Empty(t, orgRepo.ledgerCalls)
}
