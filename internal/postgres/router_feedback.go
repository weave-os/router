package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RouterFeedbackRepo atomically owns completion history, explicit ratings and delivery claims.
type RouterFeedbackRepo struct{ pool *pgxpool.Pool }

// NewRouterFeedbackRepo uses pool-backed transactions for completion and acceptance.
func NewRouterFeedbackRepo(pool *pgxpool.Pool) *RouterFeedbackRepo {
	return &RouterFeedbackRepo{pool: pool}
}

var (
	_                              proxy.RouterFeedbackStore = (*RouterFeedbackRepo)(nil)
	_                              proxy.RouterFeedbackQueue = (*RouterFeedbackRepo)(nil)
	errFeedbackCompletionDuplicate                           = errors.New("duplicate feedback completion")
)

// CompleteFeedbackRequest records one request once, allocating its completion-order position.
func (r *RouterFeedbackRepo) CompleteFeedbackRequest(ctx context.Context, p proxy.FeedbackRequest) error {
	id, err := feedbackScope(p.InstallationID, p.SessionKey, p.Role)
	if err != nil {
		return err
	}
	if p.RequestID == "" || p.ServedModel == "" || p.ServedProvider == "" {
		return errors.New("feedback completion requires served request identity")
	}
	err = pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		return completeFeedbackRequest(ctx, sqlc.New(tx), id, p)
	})
	if errors.Is(err, errFeedbackCompletionDuplicate) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("complete feedback request: %w", err)
	}
	return nil
}

func completeFeedbackRequest(ctx context.Context, q *sqlc.Queries, id uuid.UUID, p proxy.FeedbackRequest) error {
	last, err := lockFeedbackScope(ctx, q, id, p.SessionKey, p.Role)
	if err != nil {
		return err
	}
	_, err = q.GetFeedbackHistoryByRequest(ctx, sqlc.GetFeedbackHistoryByRequestParams{InstallationID: id, RequestID: p.RequestID})
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	sequence, err := q.InsertFeedbackRequestHistory(ctx, sqlc.InsertFeedbackRequestHistoryParams{
		InstallationID: id, SessionKey: p.SessionKey, Role: p.Role, Sequence: last + 1, RequestID: p.RequestID,
		ServedModel: p.ServedModel, ServedProvider: p.ServedProvider, Strategy: p.Strategy, RouteID: p.RouteID, TrainingAllowed: p.TrainingAllowed,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// Roll back the scope creation too when a concurrent duplicate won elsewhere.
		return errFeedbackCompletionDuplicate
	}
	if err != nil {
		return err
	}
	return q.UpdateFeedbackHistorySequence(ctx, sqlc.UpdateFeedbackHistorySequenceParams{InstallationID: id, SessionKey: p.SessionKey, Role: p.Role, Sequence: sequence})
}

func lockFeedbackScope(ctx context.Context, q *sqlc.Queries, id uuid.UUID, key []byte, role string) (int64, error) {
	if err := q.InsertFeedbackHistoryScope(ctx, sqlc.InsertFeedbackHistoryScopeParams{InstallationID: id, SessionKey: key, Role: role}); err != nil {
		return 0, err
	}
	return q.GetFeedbackHistoryScopeForUpdate(ctx, sqlc.GetFeedbackHistoryScopeForUpdateParams{InstallationID: id, SessionKey: key, Role: role})
}

func feedbackScope(installationID string, key []byte, role string) (uuid.UUID, error) {
	id, err := uuid.Parse(installationID)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("feedback requires a nonzero installation id")
	}
	if len(key) != sessionpin.SessionKeyLen || bytes.Equal(key, make([]byte, sessionpin.SessionKeyLen)) {
		return uuid.Nil, errors.New("feedback requires a nonzero session key")
	}
	switch role {
	case sessionpin.DefaultRole, sessionpin.DefaultRole + "_low", sessionpin.DefaultRole + "_mid", sessionpin.DefaultRole + "_high":
		return id, nil
	default:
		return uuid.Nil, errors.New("feedback requires a logical tier role")
	}
}

// AcceptRouterFeedback freezes the selector and commits its command and optional thumb together.
func (r *RouterFeedbackRepo) AcceptRouterFeedback(ctx context.Context, p proxy.RouterFeedbackEvent) (proxy.RouterFeedbackEvent, error) {
	id, err := feedbackScope(p.InstallationID, p.SessionKey, p.Role)
	if err != nil {
		return proxy.RouterFeedbackEvent{}, err
	}
	commandID, err := uuid.Parse(p.ID)
	if err != nil || commandID == uuid.Nil {
		return proxy.RouterFeedbackEvent{}, errors.New("feedback requires a command id")
	}
	if p.Sequence == 0 {
		p.Sequence = -1
	}
	if p.Sequence < -99 || p.Sequence > 99 {
		return proxy.RouterFeedbackEvent{}, errors.New("feedback selector must be between 1 and 99 in magnitude")
	}
	if p.Rating != "" && p.Rating != "up" && p.Rating != "down" {
		return proxy.RouterFeedbackEvent{}, errors.New("invalid feedback rating")
	}
	if p.Feedback == "" {
		return proxy.RouterFeedbackEvent{}, errors.New("feedback requires submission text")
	}
	var accepted proxy.RouterFeedbackEvent
	err = pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		last, err := lockFeedbackScope(ctx, q, id, p.SessionKey, p.Role)
		if err != nil {
			return err
		}
		stored, err := q.GetRouterFeedback(ctx, commandID)
		if err == nil {
			accepted = routerFeedbackEvent(stored)
			return feedbackAcceptanceScope(accepted, p)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		p.TargetSequence = 0
		p.RequestID, p.ServedModel, p.ServedProvider, p.Strategy, p.RouteID = "", "", "", "", ""
		sequence := int64(p.Sequence)
		if sequence < 0 {
			sequence = last + sequence + 1
		}
		if sequence > 0 && sequence <= last {
			// A new statement sees a completion that committed while this transaction waited for the lock.
			target, err := q.GetFeedbackHistoryBySequence(ctx, sqlc.GetFeedbackHistoryBySequenceParams{InstallationID: id, SessionKey: p.SessionKey, Role: p.Role, Sequence: sequence})
			if err != nil {
				return fmt.Errorf("read authoritative feedback history: %w", err)
			}
			p.TargetSequence = target.Sequence
			p.RequestID, p.ServedModel, p.ServedProvider, p.Strategy, p.RouteID = target.RequestID, target.ServedModel, target.ServedProvider, target.Strategy, target.RouteID
			p.TrainingAllowed = p.TrainingAllowed && target.TrainingAllowed
		} else {
			p.TrainingAllowed = false
		}
		stored, err = q.InsertRouterFeedback(ctx, sqlc.InsertRouterFeedbackParams{
			ID: commandID, InstallationID: id, SessionKey: p.SessionKey, Role: p.Role, RouterUserID: uuidOrNil(p.RouterUserID),
			ClientApp: stringPtrOrNil(p.ClientApp), SessionID: stringPtrOrNil(p.SessionID), RequestedModel: p.RequestedModel,
			ServedModel: p.ServedModel, Feedback: p.Feedback, Rating: stringPtrOrNil(p.Rating), SuggestedLabel: stringPtrOrNil(p.SuggestedLabel),
			Source: p.Source, RequestID: stringPtrOrNil(p.RequestID), RouteID: stringPtrOrNil(p.RouteID), ExternalID: p.ExternalID,
			RequestedSequence: int32(p.Sequence), TargetSequence: p.TargetSequence, Strategy: p.Strategy, ServedProvider: p.ServedProvider,
			RolloutID: p.RolloutID, TrainingAllowed: p.TrainingAllowed,
		})
		if errors.Is(err, sql.ErrNoRows) {
			stored, err = q.GetRouterFeedback(ctx, commandID)
			if err != nil {
				return err
			}
			accepted = routerFeedbackEvent(stored)
			return feedbackAcceptanceScope(accepted, p)
		}
		if err != nil {
			return err
		}
		if p.RequestID != "" && p.Rating != "" {
			comment := p.Feedback
			if (p.Rating == "up" && comment == "👍") || (p.Rating == "down" && comment == "👎") {
				comment = ""
			}
			if err := q.UpsertRequestFeedback(ctx, sqlc.UpsertRequestFeedbackParams{InstallationID: id, ExternalID: p.ExternalID, RequestID: p.RequestID, Rating: p.Rating, Comment: &comment, Source: "router-feedback-command", RouterUserID: uuidOrNil(p.RouterUserID)}); err != nil {
				return err
			}
		}
		accepted = routerFeedbackEvent(stored)
		return nil
	})
	if err != nil {
		return proxy.RouterFeedbackEvent{}, fmt.Errorf("accept router feedback: %w", err)
	}
	return accepted, nil
}

func feedbackAcceptanceScope(stored, requested proxy.RouterFeedbackEvent) error {
	if stored.InstallationID != requested.InstallationID || stored.Role != requested.Role || !bytes.Equal(stored.SessionKey, requested.SessionKey) {
		return errors.New("feedback command id belongs to another scope")
	}
	return nil
}

// ClaimRouterFeedback acquires a due command without blocking another process's claim.
func (r *RouterFeedbackRepo) ClaimRouterFeedback(ctx context.Context, token string, lease time.Duration) (proxy.RouterFeedbackEvent, bool, error) {
	id, err := uuid.Parse(token)
	if err != nil || id == uuid.Nil || lease <= 0 {
		return proxy.RouterFeedbackEvent{}, false, errors.New("invalid feedback lease")
	}
	row, err := sqlc.New(r.pool).UpdateRouterFeedbackClaim(ctx, sqlc.UpdateRouterFeedbackClaimParams{LeaseToken: id, LeaseMilliseconds: lease.Milliseconds()})
	if errors.Is(err, sql.ErrNoRows) {
		return proxy.RouterFeedbackEvent{}, false, nil
	}
	if err != nil {
		return proxy.RouterFeedbackEvent{}, false, err
	}
	return routerFeedbackEvent(row), true, nil
}

// FinishRouterFeedback settles only the current token and never changes the local rating.
func (r *RouterFeedbackRepo) FinishRouterFeedback(ctx context.Context, id, token, status, lastError string, nextAttempt time.Time) error {
	commandID, err := uuid.Parse(id)
	if err != nil {
		return err
	}
	leaseToken, err := uuid.Parse(token)
	if err != nil {
		return err
	}
	if nextAttempt.IsZero() {
		nextAttempt = time.Now()
	}
	changed, err := sqlc.New(r.pool).UpdateRouterFeedbackFinished(ctx, sqlc.UpdateRouterFeedbackFinishedParams{ID: commandID, LeaseToken: leaseToken, DeliveryStatus: status, LastError: lastError, NextAttemptAt: pgtype.Timestamptz{Time: nextAttempt, Valid: true}})
	if err != nil {
		return err
	}
	if changed != 1 {
		return proxy.ErrFeedbackLeaseLost
	}
	return nil
}

// RouterFeedbackTrainingAllowed checks current installation consent without a cache.
func (r *RouterFeedbackRepo) RouterFeedbackTrainingAllowed(ctx context.Context, installationID, externalID string) (bool, error) {
	id, err := uuid.Parse(installationID)
	if err != nil {
		return false, err
	}
	return sqlc.New(r.pool).GetRouterFeedbackTrainingAllowed(ctx, sqlc.GetRouterFeedbackTrainingAllowedParams{InstallationID: id, ExternalID: externalID})
}

func routerFeedbackEvent(row sqlc.RouterRouterFeedback) proxy.RouterFeedbackEvent {
	return proxy.RouterFeedbackEvent{
		ID: row.ID.String(), ExternalID: row.ExternalID, Sequence: int(row.RequestedSequence), TargetSequence: row.TargetSequence,
		Strategy: row.Strategy, ServedProvider: row.ServedProvider, RolloutID: row.RolloutID, TrainingAllowed: row.TrainingAllowed,
		DeliveryStatus: row.DeliveryStatus, Attempts: int(row.Attempts), LeaseToken: derefString(uuidStringPtr(row.LeaseToken)), LastError: row.LastError, CreatedAt: row.CreatedAt.Time,
		InstallationID: row.InstallationID.String(), SessionKey: row.SessionKey, Role: row.Role, RouterUserID: derefString(uuidStringPtr(row.RouterUserID)), ClientApp: derefString(row.ClientApp), SessionID: derefString(row.SessionID), RequestedModel: row.RequestedModel, ServedModel: row.ServedModel, Rating: derefString(row.Rating), SuggestedLabel: derefString(row.SuggestedLabel), Feedback: row.Feedback, Source: row.Source, RequestID: derefString(row.RequestID), RouteID: derefString(row.RouteID),
	}
}
