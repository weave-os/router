package serving

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

// InternalTestBook owns isolated test balances and inference settlement.
type InternalTestBook struct{ queries *sqlc.Queries }

// NewInternalTestBook binds isolated accounting to the primary database.
func NewInternalTestBook(pool *pgxpool.Pool) *InternalTestBook {
	return &InternalTestBook{queries: dbbudget.Queries(pool)}
}
func testBudgetSubject(owner billing.Owner) (uuid.UUID, error) {
	if err := owner.Validate(); err != nil {
		return uuid.Nil, err
	}
	if owner.Kind != billing.OwnerKindInternalTest {
		return uuid.Nil, billing.ErrOwnerKindUnsupported
	}
	return uuid.Parse(owner.TestSubjectID)
}

// Balance refuses unavailable funding and non-test owners.
func (b *InternalTestBook) Balance(ctx context.Context, owner billing.Owner) (int64, error) {
	subject, err := testBudgetSubject(owner)
	if err != nil {
		return 0, err
	}
	return b.queries.GetInternalTestBalance(ctx, subject)
}

// Debit records isolated spend and key metering in one database statement.
func (b *InternalTestBook) Debit(ctx context.Context, debit billing.PrepaidDebit) (int64, error) {
	subject, err := testBudgetSubject(debit.Owner)
	if err != nil {
		return 0, err
	}
	key, err := uuid.Parse(debit.APIKeyID)
	if err != nil {
		return 0, err
	}
	if debit.DeltaUsdMicros > 0 || debit.FeeUsdMicros != 0 {
		return 0, errors.New("invalid internal test debit")
	}
	return b.queries.InsertInternalTestInferenceDebit(ctx, sqlc.InsertInternalTestInferenceDebitParams{SubjectID: subject, DeltaUsdMicros: debit.DeltaUsdMicros, RouterRequestID: debit.RouterRequestID, RouterModel: debit.RouterModel, APIKeyID: key})
}
