package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions"
)

func (s *codexResetStore) RecordCodexExhaustion(ctx context.Context, owner auth.SubscriptionOwner, lease subscriptions.Lease, resetAt time.Time) (bool, error) {
	subscriber, err := uuid.Parse(owner.SubscriberID)
	if err != nil {
		return false, err
	}
	account, err := uuid.Parse(lease.AccountID)
	if err != nil {
		return false, err
	}
	rows, err := dbbudget.Queries(s.tx).UpdateCodexExhaustionForGeneration(ctx, sqlc.UpdateCodexExhaustionForGenerationParams{
		SubscriberID: subscriber, AccountID: account, QuotaResetGeneration: lease.QuotaResetGeneration,
		ResetAt: pgtype.Timestamp{Time: resetAt.UTC(), Valid: true},
	})
	return rows != 0, err
}
