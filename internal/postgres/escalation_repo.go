package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/sqlc"

	"github.com/cenkalti/backoff/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EscalationRepo uses short transactions to persist classifier state and claim leases.
type EscalationRepo struct{ pool *pgxpool.Pool }

// NewEscalationRepo wires a pool because state/checkpoint writes commit atomically.
func NewEscalationRepo(pool *pgxpool.Pool) *EscalationRepo { return &EscalationRepo{pool: pool} }

var _ escalation.Store = (*EscalationRepo)(nil)

// Claim returns false when another observer owns the session's unexpired lease.
func (r *EscalationRepo) Claim(ctx context.Context, scope [32]byte, installationID, token string, boundary [32]byte) (escalation.Session, bool, error) {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return escalation.Session{}, false, fmt.Errorf("parse escalation installation id: %w", err)
	}
	leaseUUID, err := uuid.Parse(token)
	if err != nil {
		return escalation.Session{}, false, fmt.Errorf("parse escalation lease token: %w", err)
	}
	var session escalation.Session
	acquired := false
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		// A lifetime can expire between deletion and claiming. One immediate
		// retry removes that lifetime instead of resurrecting its checkpoints.
		encodedSessionState, claimErr := backoff.Retry(ctx, func() ([]byte, error) {
			if deleteErr := queries.DeleteExpiredEscalationSession(ctx, scope[:]); deleteErr != nil {
				return nil, backoff.Permanent(deleteErr)
			}
			encoded, upsertErr := queries.UpsertEscalationSessionClaim(ctx, sqlc.UpsertEscalationSessionClaimParams{Scope: scope[:], InstallationID: installationUUID, LeaseToken: leaseUUID, Boundary: boundary[:]})
			if errors.Is(upsertErr, sql.ErrNoRows) {
				expired, expiryErr := queries.GetEscalationSessionExpired(ctx, scope[:])
				if expiryErr != nil {
					return nil, backoff.Permanent(expiryErr)
				}
				if !expired {
					return nil, backoff.Permanent(upsertErr)
				}
			}
			if upsertErr != nil && !errors.Is(upsertErr, sql.ErrNoRows) {
				return nil, backoff.Permanent(upsertErr)
			}
			return encoded, upsertErr
		}, backoff.WithBackOff(backoff.NewConstantBackOff(0)), backoff.WithMaxTries(2))
		if errors.Is(claimErr, sql.ErrNoRows) {
			return nil
		}
		if claimErr != nil {
			return claimErr
		}
		decodeErr := json.Unmarshal(encodedSessionState, &session)
		if decodeErr != nil {
			return fmt.Errorf("decode escalation session: %w", decodeErr)
		}
		acquired = true
		return nil
	})
	if err != nil {
		return escalation.Session{}, false, fmt.Errorf("claim escalation session: %w", err)
	}
	return session, acquired, nil
}

// Checkpoint reads only boundaries belonging to the current session lifetime.
func (r *EscalationRepo) Checkpoint(ctx context.Context, scope, boundary [32]byte) (escalation.Checkpoint, bool, error) {
	encodedCheckpoint, err := sqlc.New(r.pool).GetEscalationCheckpoint(ctx, sqlc.GetEscalationCheckpointParams{Scope: scope[:], Boundary: boundary[:]})
	if errors.Is(err, sql.ErrNoRows) {
		return escalation.Checkpoint{}, false, nil
	}
	if err != nil {
		return escalation.Checkpoint{}, false, fmt.Errorf("read escalation checkpoint: %w", err)
	}
	var checkpoint escalation.Checkpoint
	err = json.Unmarshal(encodedCheckpoint, &checkpoint)
	if err != nil {
		return escalation.Checkpoint{}, false, fmt.Errorf("decode escalation checkpoint: %w", err)
	}
	return checkpoint, true, nil
}

// Commit advances one ordinal and records its boundary in the same transaction.
func (r *EscalationRepo) Commit(ctx context.Context, scope, boundary [32]byte, token string, session escalation.Session, checkpoint escalation.Checkpoint) error {
	if session.Ordinal < 1 || checkpoint.Ordinal != session.Ordinal {
		return fmt.Errorf("escalation checkpoint ordinal does not match session")
	}
	leaseUUID, err := uuid.Parse(token)
	if err != nil {
		return fmt.Errorf("parse escalation lease token: %w", err)
	}
	encodedSession, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode escalation session: %w", err)
	}
	encodedCheckpoint, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode escalation checkpoint: %w", err)
	}
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		updated, updateErr := queries.UpdateEscalationSessionCommit(ctx, sqlc.UpdateEscalationSessionCommitParams{Scope: scope[:], LeaseToken: leaseUUID, Ordinal: session.Ordinal, SessionState: encodedSession})
		if updateErr != nil {
			return updateErr
		}
		if updated != 1 {
			return escalation.ErrLeaseLost
		}
		return queries.InsertEscalationCheckpoint(ctx, sqlc.InsertEscalationCheckpointParams{Scope: scope[:], Boundary: boundary[:], Checkpoint: encodedCheckpoint})
	})
	if err != nil {
		return fmt.Errorf("commit escalation observation: %w", err)
	}
	return nil
}

// Release cannot clear a successor's lease, even after the caller's timeout.
func (r *EscalationRepo) Release(ctx context.Context, scope [32]byte, token string) error {
	leaseUUID, err := uuid.Parse(token)
	if err != nil {
		return fmt.Errorf("parse escalation lease token: %w", err)
	}
	err = sqlc.New(r.pool).UpdateEscalationSessionRelease(ctx, sqlc.UpdateEscalationSessionReleaseParams{Scope: scope[:], LeaseToken: leaseUUID})
	if err != nil {
		return fmt.Errorf("release escalation session: %w", err)
	}
	return nil
}

// Invalidate resets continuity and atomically releases failedToken when it owns
// the lease. An empty token preserves a distinct active observer.
func (r *EscalationRepo) Invalidate(ctx context.Context, scope, boundary [32]byte, failedToken string) error {
	failedLeaseUUID := uuid.Nil
	if failedToken != "" {
		var err error
		failedLeaseUUID, err = uuid.Parse(failedToken)
		if err != nil {
			return fmt.Errorf("parse failed escalation lease token: %w", err)
		}
	}
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		_, lockErr := queries.GetEscalationSessionForInvalidation(ctx, scope[:])
		if errors.Is(lockErr, sql.ErrNoRows) {
			return nil
		}
		if lockErr != nil {
			return lockErr
		}
		// Commit inserts the checkpoint under this same session lock. A second
		// statement sees it even when the lock request waited for that commit.
		return queries.UpdateEscalationSessionInvalidated(ctx, sqlc.UpdateEscalationSessionInvalidatedParams{Scope: scope[:], Boundary: boundary[:], FailedLeaseToken: failedLeaseUUID})
	})
	if err != nil {
		return fmt.Errorf("invalidate escalation session: %w", err)
	}
	return nil
}

// SaveOutcome ignores stale completions and sessions already claimed for another observation.
func (r *EscalationRepo) SaveOutcome(ctx context.Context, scope [32]byte, ordinal int64, outcome escalation.PreviousOutcome) error {
	encodedOutcome, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("encode escalation outcome: %w", err)
	}
	err = sqlc.New(r.pool).UpdateEscalationSessionOutcome(ctx, sqlc.UpdateEscalationSessionOutcomeParams{Scope: scope[:], Ordinal: ordinal, PreviousOutcome: encodedOutcome})
	if err != nil {
		return fmt.Errorf("save escalation outcome: %w", err)
	}
	return nil
}

// SaveContinuation binds an immutable response identity only while its completed
// turn is still current. Stored histories share the owning session's retention.
func (r *EscalationRepo) SaveContinuation(ctx context.Context, activation [32]byte, responseID string, scope [32]byte, ordinal int64, history json.RawMessage) error {
	if responseID == "" {
		return errors.New("escalation continuation has no response id")
	}
	responseDigest := sha256.Sum256([]byte(responseID))
	err := sqlc.New(r.pool).InsertEscalationContinuation(ctx, sqlc.InsertEscalationContinuationParams{
		Scope: scope[:], Ordinal: ordinal, Activation: activation[:], ResponseDigest: responseDigest[:], History: history,
	})
	if err != nil {
		return fmt.Errorf("save escalation continuation: %w", err)
	}
	return nil
}

// Continuation resolves a response without relying on a delta request's thread key.
func (r *EscalationRepo) Continuation(ctx context.Context, activation [32]byte, responseID string) ([32]byte, json.RawMessage, bool, error) {
	if responseID == "" {
		return [32]byte{}, nil, false, nil
	}
	responseDigest := sha256.Sum256([]byte(responseID))
	continuation, err := sqlc.New(r.pool).GetEscalationContinuation(ctx, sqlc.GetEscalationContinuationParams{
		Activation: activation[:], ResponseDigest: responseDigest[:],
	})
	if errors.Is(err, sql.ErrNoRows) {
		return [32]byte{}, nil, false, nil
	}
	if err != nil {
		return [32]byte{}, nil, false, fmt.Errorf("read escalation continuation: %w", err)
	}
	var scope [32]byte
	copy(scope[:], continuation.Scope)
	return scope, continuation.History, true, nil
}

// SweepExpired removes feature state and its checkpoint identities together.
func (r *EscalationRepo) SweepExpired(ctx context.Context) error {
	err := sqlc.New(r.pool).DeleteExpiredEscalationSessions(ctx)
	if err != nil {
		return fmt.Errorf("sweep escalation sessions: %w", err)
	}
	return nil
}
