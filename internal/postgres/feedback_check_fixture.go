package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FeedbackCheckFixture confines integration-only locks and cleanup to one disposable installation.
type FeedbackCheckFixture struct {
	pool       *pgxpool.Pool
	id         uuid.UUID
	externalID string
}

// NewFeedbackCheckFixture binds fixture operations to the installation the script created.
func NewFeedbackCheckFixture(pool *pgxpool.Pool, installation auth.Installation) *FeedbackCheckFixture {
	return &FeedbackCheckFixture{pool: pool, id: uuid.MustParse(installation.ID), externalID: installation.ExternalID}
}

// Cleanup deletes the fixture installation and verifies its feedback cascades.
func (f *FeedbackCheckFixture) Cleanup(ctx context.Context) error {
	q := sqlc.New(f.pool)
	n, err := q.DeleteFeedbackCheckFixture(ctx, sqlc.DeleteFeedbackCheckFixtureParams{InstallationID: f.id, ExternalID: f.externalID})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("feedback cleanup removed %d installations", n)
	}
	left, err := q.GetFeedbackCheckRemaining(ctx, f.id)
	if err != nil {
		return err
	}
	if left != 0 {
		return fmt.Errorf("feedback cleanup left %d rows", left)
	}
	return nil
}

// HoldCompletion pauses the production completion transaction before commit to exercise waiting readers.
func (f *FeedbackCheckFixture) HoldCompletion(ctx context.Context, p proxy.FeedbackRequest, paused func() error) error {
	if p.InstallationID != f.id.String() {
		return errors.New("completion is outside fixture")
	}
	return pgx.BeginTxFunc(ctx, f.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := completeFeedbackRequest(ctx, sqlc.New(tx), f.id, p); err != nil {
			return err
		}
		return paused()
	})
}

// HoldRating locks an existing fixture rating so a competing acceptance can time out and roll back.
func (f *FeedbackCheckFixture) HoldRating(ctx context.Context, requestID string, paused func() error) error {
	return pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		_, err := sqlc.New(tx).GetFeedbackCheckRatingForUpdate(ctx, sqlc.GetFeedbackCheckRatingForUpdateParams{InstallationID: f.id, ExternalID: f.externalID, RequestID: requestID})
		if err != nil {
			return err
		}
		return paused()
	})
}

// BlockedReaders counts blockers of the script's dedicated reader connection.
func (f *FeedbackCheckFixture) BlockedReaders(ctx context.Context, pid int32) (int64, error) {
	return sqlc.New(f.pool).GetFeedbackCheckBlocked(ctx, pid)
}

// MakeDue expires only this fixture's pending delivery leases and backoffs.
func (f *FeedbackCheckFixture) MakeDue(ctx context.Context) error {
	return sqlc.New(f.pool).UpdateFeedbackCheckDue(ctx, sqlc.UpdateFeedbackCheckDueParams{InstallationID: f.id, ExternalID: f.externalID})
}

// SetTrainingAllowed changes consent only for the fixture installation.
func (f *FeedbackCheckFixture) SetTrainingAllowed(ctx context.Context, allowed bool) error {
	return sqlc.New(f.pool).UpdateFeedbackCheckPermission(ctx, sqlc.UpdateFeedbackCheckPermissionParams{InstallationID: f.id, ExternalID: f.externalID, Allowed: allowed})
}

// Command reads a fixture-owned command to distinguish rollback from saved unavailability.
func (f *FeedbackCheckFixture) Command(ctx context.Context, id string) (proxy.RouterFeedbackEvent, bool, error) {
	commandID, err := uuid.Parse(id)
	if err != nil {
		return proxy.RouterFeedbackEvent{}, false, err
	}
	row, err := sqlc.New(f.pool).GetRouterFeedback(ctx, commandID)
	if errors.Is(err, sql.ErrNoRows) {
		return proxy.RouterFeedbackEvent{}, false, nil
	}
	if err != nil {
		return proxy.RouterFeedbackEvent{}, false, err
	}
	if row.InstallationID != f.id {
		return proxy.RouterFeedbackEvent{}, false, errors.New("command is outside fixture")
	}
	return routerFeedbackEvent(row), true, nil
}

// History returns the fixture's committed sequence numbers for one logical scope.
func (f *FeedbackCheckFixture) History(ctx context.Context, key []byte, role string) ([]proxy.FeedbackRequest, error) {
	rows, err := sqlc.New(f.pool).GetFeedbackCheckHistory(ctx, sqlc.GetFeedbackCheckHistoryParams{InstallationID: f.id, ExternalID: f.externalID, SessionKey: key, Role: role})
	if err != nil {
		return nil, err
	}
	out := make([]proxy.FeedbackRequest, len(rows))
	for i, r := range rows {
		out[i] = proxy.FeedbackRequest{InstallationID: r.InstallationID.String(), SessionKey: r.SessionKey, Role: r.Role, RequestID: r.RequestID, Sequence: r.Sequence, CompletedAt: r.CompletedAt.Time, ServedModel: r.ServedModel, ServedProvider: r.ServedProvider, Strategy: r.Strategy, RouteID: r.RouteID, TrainingAllowed: r.TrainingAllowed}
	}
	return out, nil
}

// BackendPID identifies the fixture's dedicated database connection.
func (f *FeedbackCheckFixture) BackendPID(ctx context.Context) (int32, error) {
	return sqlc.New(f.pool).GetFeedbackCheckPID(ctx)
}
