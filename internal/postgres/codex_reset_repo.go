package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
	"weave-os/router/internal/subscriptions"
)

type codexResetStore struct{ tx sqlc.DBTX }

// NewCodexResetStore coordinates earned-reset redemption across router replicas.
func NewCodexResetStore(tx sqlc.DBTX) subscriptions.ResetStore {
	return &codexResetStore{tx: tx}
}

func (s *codexResetStore) AcquireCodexReset(ctx context.Context, subscriberID, leaseID string, ttl time.Duration) (subscriptions.ResetClaim, bool, error) {
	claim := subscriptions.ResetClaim{SubscriberID: subscriberID, LeaseID: leaseID}
	subscriber, lease, err := resetClaimIDs(claim)
	if err != nil {
		return claim, false, err
	}
	pending, err := dbbudget.Queries(s.tx).UpsertCodexResetLease(ctx, sqlc.UpsertCodexResetLeaseParams{
		SubscriberID: subscriber, LeaseID: lease, LeaseSeconds: int64(ttl / time.Second),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return claim, false, nil
	}
	if err != nil {
		return claim, false, err
	}
	if pending.AccountID.Valid {
		claim.AccountID = uuid.UUID(pending.AccountID.Bytes).String()
		claim.RequestID = uuid.UUID(pending.RequestID.Bytes).String()
		claim.CreditID = *pending.CreditID
	}
	return claim, true, nil
}

func (s *codexResetStore) SelectCodexReset(ctx context.Context, claim subscriptions.ResetClaim) error {
	subscriber, lease, err := resetClaimIDs(claim)
	if err != nil {
		return err
	}
	account, err := uuid.Parse(claim.AccountID)
	if err != nil {
		return err
	}
	request, err := uuid.Parse(claim.RequestID)
	if err != nil {
		return err
	}
	return checkResetClaimUpdate(dbbudget.Queries(s.tx).UpdateCodexResetSelection(ctx, sqlc.UpdateCodexResetSelectionParams{
		SubscriberID: subscriber, LeaseID: lease, AccountID: account, CreditID: claim.CreditID, RequestID: request,
	}))
}

func (s *codexResetStore) RecoverCodexAccount(ctx context.Context, claim subscriptions.ResetClaim, accountID string) error {
	subscriber, lease, err := resetClaimIDs(claim)
	if err != nil {
		return err
	}
	account, err := uuid.Parse(accountID)
	if err != nil {
		return err
	}
	return checkResetClaimUpdate(dbbudget.Queries(s.tx).UpdateCodexResetRecovery(ctx, sqlc.UpdateCodexResetRecoveryParams{
		SubscriberID: subscriber, LeaseID: lease, AccountID: account,
	}))
}

func (s *codexResetStore) ClearCodexReset(ctx context.Context, claim subscriptions.ResetClaim) error {
	subscriber, lease, err := resetClaimIDs(claim)
	if err != nil {
		return err
	}
	return checkResetClaimUpdate(dbbudget.Queries(s.tx).UpdateCodexResetClear(ctx, sqlc.UpdateCodexResetClearParams{
		SubscriberID: subscriber, LeaseID: lease,
	}))
}

func (s *codexResetStore) ReleaseCodexReset(ctx context.Context, claim subscriptions.ResetClaim) error {
	subscriber, lease, err := resetClaimIDs(claim)
	if err != nil {
		return err
	}
	return checkResetClaimUpdate(dbbudget.Queries(s.tx).UpdateCodexResetRelease(ctx, sqlc.UpdateCodexResetReleaseParams{
		SubscriberID: subscriber, LeaseID: lease,
	}))
}

func resetClaimIDs(claim subscriptions.ResetClaim) (subscriber, lease uuid.UUID, err error) {
	subscriber, err = uuid.Parse(claim.SubscriberID)
	if err != nil {
		return subscriber, lease, err
	}
	lease, err = uuid.Parse(claim.LeaseID)
	return subscriber, lease, err
}

func checkResetClaimUpdate(rows int64, err error) error {
	if err != nil {
		return err
	}
	if rows == 0 {
		return subscriptions.ErrResetClaimLost
	}
	return nil
}
