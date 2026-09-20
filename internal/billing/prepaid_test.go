package billing_test

import (
	"context"
	"sync"
	"testing"

	"weave-os/router/internal/billing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSubscriberBook is an in-memory prepaid book keyed by credential
// subject, standing in for the Postgres subscriber store.
type fakeSubscriberBook struct {
	mu       sync.Mutex
	balances map[string]int64
	debits   []billing.PrepaidDebit
}

func newFakeSubscriberBook(balances map[string]int64) *fakeSubscriberBook {
	return &fakeSubscriberBook{balances: balances}
}

func (b *fakeSubscriberBook) Balance(_ context.Context, owner billing.Owner) (int64, error) {
	if owner.Kind != billing.OwnerKindSubscriber {
		return 0, billing.ErrOwnerKindUnsupported
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	balance, ok := b.balances[owner.SubscriberID]
	if !ok {
		return 0, billing.ErrBalanceRowMissing
	}
	return balance, nil
}

func (b *fakeSubscriberBook) Debit(_ context.Context, debit billing.PrepaidDebit) (int64, error) {
	if debit.Owner.Kind != billing.OwnerKindSubscriber {
		return 0, billing.ErrOwnerKindUnsupported
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	balance, ok := b.balances[debit.Owner.SubscriberID]
	if !ok {
		return 0, billing.ErrBalanceRowMissing
	}
	balance += debit.DeltaUsdMicros
	b.balances[debit.Owner.SubscriberID] = balance
	b.debits = append(b.debits, debit)
	return balance, nil
}

func TestOwnerValidateRejectsCrossedIdentifiers(t *testing.T) {
	t.Parallel()

	require.NoError(t, billing.OrganizationOwner("org_1").Validate())
	require.NoError(t, billing.SubscriberOwner("sub_1").Validate())

	// A subscriber id smuggled in as an organization id, and vice versa: the
	// two identifier spaces must never be interchangeable.
	crossed := []billing.Owner{
		{Kind: billing.OwnerKindOrganization, OrganizationID: "org_1", SubscriberID: "sub_1"},
		{Kind: billing.OwnerKindSubscriber, OrganizationID: "org_1", SubscriberID: "sub_1"},
		{Kind: billing.OwnerKindOrganization},
		{Kind: billing.OwnerKindSubscriber},
		{Kind: "account", OrganizationID: "org_1"},
	}
	for _, owner := range crossed {
		assert.ErrorIs(t, owner.Validate(), billing.ErrInvalidOwner, "owner %+v must not validate", owner)
	}
}

func TestPrepaidOrganizationOwnerUsesOrganizationRepo(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{balanceMicros: 5_000_000, balanceRowExists: true}
	svc := billing.NewService(repo)

	balance, err := svc.PrepaidBalance(context.Background(), billing.OrganizationOwner("org_1"))
	require.NoError(t, err)
	assert.Equal(t, int64(5_000_000), balance)

	after, err := svc.DebitPrepaid(context.Background(), billing.PrepaidDebit{
		Owner:              billing.OrganizationOwner("org_1"),
		DeltaUsdMicros:     -1_000_000,
		NotionalCostMicros: 1_000_000,
		EntryType:          billing.EntryTypeInference,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(4_000_000), after)
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, "org_1", repo.ledgerCalls[0].OrganizationID)
}

func TestOrganizationBookRejectsCrossedOwnerWhenUsedDirectly(t *testing.T) {
	t.Parallel()

	// The book is exported, so it can be handed an owner that never passed
	// through PrepaidBooks' dispatch; a crossed owner must still not spend.
	repo := &fakeRepo{balanceMicros: 5_000_000, balanceRowExists: true}
	book := billing.OrganizationBook(repo)
	crossed := billing.Owner{
		Kind:           billing.OwnerKindOrganization,
		OrganizationID: "org_1",
		SubscriberID:   "11111111-1111-4111-8111-111111111111",
	}

	_, err := book.Balance(context.Background(), crossed)
	assert.ErrorIs(t, err, billing.ErrInvalidOwner)

	_, err = book.Debit(context.Background(), billing.PrepaidDebit{
		Owner:          crossed,
		DeltaUsdMicros: -1_000_000,
		EntryType:      billing.EntryTypeInference,
	})
	assert.ErrorIs(t, err, billing.ErrInvalidOwner)
	assert.Zero(t, repo.debitCalls.Load())
	assert.Equal(t, int64(5_000_000), repo.balanceMicros)
}

func TestPrepaidSubscriberOwnerUnsupportedWithoutSubscriberBook(t *testing.T) {
	t.Parallel()

	// Self-hosted and organization-only deployments never bind a subscriber
	// book; a subscriber owner must fail closed instead of reaching the
	// organization's money.
	repo := &fakeRepo{balanceMicros: 5_000_000, balanceRowExists: true}
	svc := billing.NewService(repo)

	_, err := svc.PrepaidBalance(context.Background(), billing.SubscriberOwner("sub_1"))
	assert.ErrorIs(t, err, billing.ErrOwnerKindUnsupported)

	_, err = svc.DebitPrepaid(context.Background(), billing.PrepaidDebit{
		Owner:          billing.SubscriberOwner("sub_1"),
		DeltaUsdMicros: -1_000_000,
		EntryType:      billing.EntryTypeInference,
	})
	assert.ErrorIs(t, err, billing.ErrOwnerKindUnsupported)
	assert.Zero(t, repo.debitCalls.Load())
	assert.Equal(t, int64(5_000_000), repo.balanceMicros)
}

func TestPrepaidSubscribersInOneOrganizationCannotSpendEachOther(t *testing.T) {
	t.Parallel()

	// Two colleagues on individual plans inside the same organization, plus
	// the organization's own balance.
	const subscriberA = "11111111-1111-4111-8111-111111111111"
	const subscriberB = "22222222-2222-4222-8222-222222222222"
	subscribers := newFakeSubscriberBook(map[string]int64{
		subscriberA: 3_000_000,
		subscriberB: 7_000_000,
	})
	orgRepo := &fakeRepo{balanceMicros: 9_000_000, balanceRowExists: true}
	svc := billing.NewService(orgRepo).WithSubscriberPrepaid(subscribers)

	after, err := svc.DebitPrepaid(context.Background(), billing.PrepaidDebit{
		Owner:              billing.SubscriberOwner(subscriberA),
		DeltaUsdMicros:     -1_000_000,
		NotionalCostMicros: 1_000_000,
		EntryType:          billing.EntryTypeInference,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2_000_000), after)

	balanceB, err := svc.PrepaidBalance(context.Background(), billing.SubscriberOwner(subscriberB))
	require.NoError(t, err)
	assert.Equal(t, int64(7_000_000), balanceB, "subscriber B must be untouched by subscriber A's debit")

	// Nor did the shared organization balance move, and no organization
	// ledger row was written for an individual plan's turn.
	assert.Equal(t, int64(9_000_000), orgRepo.balanceMicros)
	assert.Zero(t, orgRepo.debitCalls.Load())
	require.Len(t, subscribers.debits, 1)
	assert.Equal(t, subscriberA, subscribers.debits[0].Owner.SubscriberID)
}

func TestPrepaidOrganizationDebitNeverReachesSubscriberFunds(t *testing.T) {
	t.Parallel()

	const subscriberA = "11111111-1111-4111-8111-111111111111"
	subscribers := newFakeSubscriberBook(map[string]int64{subscriberA: 3_000_000})
	orgRepo := &fakeRepo{balanceMicros: 9_000_000, balanceRowExists: true}
	svc := billing.NewService(orgRepo).WithSubscriberPrepaid(subscribers)

	_, err := svc.DebitPrepaid(context.Background(), billing.PrepaidDebit{
		Owner:              billing.OrganizationOwner("org_1"),
		DeltaUsdMicros:     -1_000_000,
		NotionalCostMicros: 1_000_000,
		EntryType:          billing.EntryTypeInference,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(8_000_000), orgRepo.balanceMicros)
	assert.Empty(t, subscribers.debits)
	assert.Equal(t, int64(3_000_000), subscribers.balances[subscriberA])
}
