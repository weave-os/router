package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriberCreditSettlementReplaySurvivesConcurrentFinalization(t *testing.T) {
	pool := testPool(t)
	installationID, _ := seedInstallation(t, pool)
	subscriberID := seedFundedSubscriber(t, pool, installationID, 10_000_000)
	repo := postgres.NewSubscriberCreditRepo(pool)
	authorization, err := repo.Authorize(context.Background(), billing.PrepaidAuthorizationRequest{
		Owner:               billing.SubscriberOwner(subscriberID.String()),
		ActionID:            "req_finalize_replay:prepaid-hold",
		RouterRequestID:     "req_finalize_replay",
		RequestedModel:      entitlement.ModelUnresolved,
		UpperBoundUsdMicros: 4_000_000,
	})
	require.NoError(t, err)
	settlement := billing.PrepaidSettlement{
		AuthorizationActionID: authorization.ActionID,
		ActionID:              "req_finalize_replay:1",
		RouterRequestID:       "req_finalize_replay",
		ServedModel:           "deepseek-v3.2",
		RetailUsdMicros:       1_500_000,
		CapacitySource:        entitlement.CapacitySourcePrepaid,
	}
	barrier := &settlementReplayBarrier{
		actionID: settlement.ActionID,
		waiting:  make(chan struct{}),
		resume:   make(chan struct{}),
	}
	replayRepo := postgres.NewSubscriberCreditRepo(&settlementReplayBarrierDB{Pool: pool, barrier: barrier})
	type settlementResult struct {
		balance int64
		err     error
	}
	replay := make(chan settlementResult, 1)
	go func() {
		balance, err := replayRepo.Settle(context.Background(), settlement)
		replay <- settlementResult{balance: balance, err: err}
	}()
	defer barrier.unblock()

	select {
	case <-barrier.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("settlement replay did not reach its first ledger lookup")
	}

	settledBalance, err := repo.Settle(context.Background(), settlement)
	require.NoError(t, err)
	finalBalance, err := repo.Finalize(context.Background(), authorization.ActionID)
	require.NoError(t, err)
	barrier.unblock()

	outcome := <-replay
	require.NoError(t, outcome.err)
	assert.Equal(t, settledBalance, outcome.balance)
	assert.Equal(t, settledBalance, finalBalance)
	assert.Equal(t, 1, subscriberLedgerCount(t, pool, subscriberID))
}

type settlementReplayBarrierDB struct {
	*pgxpool.Pool
	barrier *settlementReplayBarrier
}

func (db *settlementReplayBarrierDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &settlementReplayBarrierTx{Tx: tx, barrier: db.barrier}, nil
}

type settlementReplayBarrierTx struct {
	pgx.Tx
	barrier *settlementReplayBarrier
}

func (tx *settlementReplayBarrierTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	if !strings.Contains(query, "GetSubscriberCreditSettlement") || len(args) != 1 || args[0] != tx.barrier.actionID {
		return row
	}
	var pause bool
	tx.barrier.firstLookup.Do(func() { pause = true })
	if !pause {
		return row
	}
	return settlementReplayBarrierRow{Row: row, barrier: tx.barrier}
}

type settlementReplayBarrierRow struct {
	pgx.Row
	barrier *settlementReplayBarrier
}

func (row settlementReplayBarrierRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	close(row.barrier.waiting)
	<-row.barrier.resume
	return err
}

type settlementReplayBarrier struct {
	actionID    string
	waiting     chan struct{}
	resume      chan struct{}
	firstLookup sync.Once
	resumeOnce  sync.Once
}

func (barrier *settlementReplayBarrier) unblock() {
	barrier.resumeOnce.Do(func() { close(barrier.resume) })
}
