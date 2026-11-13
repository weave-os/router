package billing_test

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"
)

type testBudgetBook struct {
	balance int64
	debits  []billing.PrepaidDebit
}

func (b *testBudgetBook) Balance(context.Context, billing.Owner) (int64, error) {
	return b.balance, nil
}
func (b *testBudgetBook) Debit(_ context.Context, debit billing.PrepaidDebit) (int64, error) {
	b.debits = append(b.debits, debit)
	b.balance += debit.DeltaUsdMicros
	return b.balance, nil
}
func TestInternalTestDebitNeverSettlesSubscriberAllowanceOrCustomerPrepaid(t *testing.T) {
	organization := &fakeRepo{balanceMicros: 999000000, hasOverride: true}
	book := &testBudgetBook{balance: 1000000}
	service := billing.NewService(organization).WithInternalTestPrepaid(book)
	ctx := requestcontext.WithInternalTestIdentity(context.Background(), requestcontext.InternalTestIdentity{SubjectID: "test-subject", SessionID: "session"})
	// Even an accidentally attached subscriber coverage cannot select its book.
	ctx = entitlement.WithCoverage(ctx, entitlement.Coverage{})
	parameters := billing.DebitInferenceParams{OrganizationID: "customer-org", RouterRequestID: "request", Model: "model", APIKeyID: "test-key", InputTokens: 1000000, Pricing: catalog.Pricing{InputUSDPer1M: 0.25}}
	balance, err := service.DebitForInference(ctx, parameters)
	require.NoError(t, err)
	require.Equal(t, int64(750000), balance)
	require.Len(t, book.debits, 1)
	require.Equal(t, billing.OwnerKindInternalTest, book.debits[0].Owner.Kind)
	require.Equal(t, "test-subject", book.debits[0].Owner.TestSubjectID)
	require.Empty(t, book.debits[0].Owner.OrganizationID)
	require.Empty(t, book.debits[0].Owner.SubscriberID)
	require.Empty(t, organization.ledgerCalls)
	for _, override := range []billing.DebitInferenceParams{{HasOverride: true}, {SubscriptionServed: true}, {ByokServed: true}} {
		_, err = service.DebitForInference(ctx, override)
		require.Error(t, err)
	}
	require.Len(t, book.debits, 1)
}
func TestMissingInternalTestBookFailsWithoutCustomerFallback(t *testing.T) {
	organization := &fakeRepo{balanceMicros: 999000000}
	service := billing.NewService(organization)
	ctx := requestcontext.WithInternalTestIdentity(context.Background(), requestcontext.InternalTestIdentity{SubjectID: "test-subject"})
	_, err := service.DebitForInference(ctx, billing.DebitInferenceParams{OrganizationID: "customer-org"})
	require.ErrorIs(t, err, billing.ErrOwnerKindUnsupported)
	require.Empty(t, organization.ledgerCalls)
}
