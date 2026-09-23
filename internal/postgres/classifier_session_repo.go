package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/semaphore"

	"weave-os/router/internal/router"
	"weave-os/router/internal/sqlc"
)

const classifierTransactionLimit = 2

// ClassifierSessionRepo keeps thread admission and prediction commits on primary.
type ClassifierSessionRepo struct {
	pool         *pgxpool.Pool
	transactions *semaphore.Weighted
}

// NewClassifierSessionRepo requires the writable router database.
func NewClassifierSessionRepo(pool *pgxpool.Pool) *ClassifierSessionRepo {
	return &ClassifierSessionRepo{pool: pool, transactions: semaphore.NewWeighted(classifierTransactionLimit)}
}

// Create returns the original binding on an idempotent handshake retry.
func (r *ClassifierSessionRepo) Create(ctx context.Context, thread router.ClassifierThread) (router.ClassifierThread, error) {
	stored, err := sqlc.New(r.pool).InsertClassifierThread(ctx, sqlc.InsertClassifierThreadParams{
		ThreadID: thread.ThreadID, InstallationID: thread.InstallationID,
		CredentialSha256: thread.CredentialSHA256[:], RequestID: thread.RequestID,
		Release: thread.Release, ReleaseSha256: thread.ReleaseSHA256,
		SelectionPolicySha256: thread.SelectionPolicySHA256,
		ExpiresAt:             pgtype.Timestamptz{Time: thread.ExpiresAt, Valid: true},
	})
	if err != nil {
		return router.ClassifierThread{}, err
	}
	thread.ThreadID, thread.Release, thread.ReleaseSHA256 = stored.ThreadID, stored.Release, stored.ReleaseSha256
	thread.SelectionPolicySHA256 = stored.SelectionPolicySha256
	thread.ExpiresAt = stored.ExpiresAt.Time
	return thread, nil
}

// WithThread holds a primary row lock until the prediction is committed.
func (r *ClassifierSessionRepo) WithThread(ctx context.Context, thread router.ClassifierThread, classify func(router.ClassifierTurnStore) error) error {
	// Bound inference and row-lock waiters before taking a shared connection.
	if !r.transactions.TryAcquire(1) {
		return fmt.Errorf("classifier transaction capacity reached: %w", router.ErrClassifierUnavailable)
	}
	defer r.transactions.Release(1)
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		storedThread, err := queries.GetClassifierThreadForUpdate(ctx, sqlc.GetClassifierThreadForUpdateParams{
			ThreadID: thread.ThreadID, InstallationID: thread.InstallationID,
			CredentialSha256: thread.CredentialSHA256[:], Release: thread.Release, ReleaseSha256: thread.ReleaseSHA256,
			SelectionPolicySha256: thread.SelectionPolicySHA256,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return router.ErrClassifierThreadInvalid
		}
		if err != nil {
			return err
		}
		return classify(classifierTurnRepo{queries: queries, threadID: thread.ThreadID,
			prefixCheckpoint: router.ClassifierPrefixCheckpoint{MessageCount: int(storedThread.PrefixMessageCount), Digest: storedThread.PrefixDigest}})
	})
}

type classifierTurnRepo struct {
	queries          *sqlc.Queries
	threadID         uuid.UUID
	prefixCheckpoint router.ClassifierPrefixCheckpoint
}

func (r classifierTurnRepo) PrefixCheckpoint() router.ClassifierPrefixCheckpoint {
	return r.prefixCheckpoint
}

func (r classifierTurnRepo) SetPrefixCheckpoint(ctx context.Context, prefixCheckpoint router.ClassifierPrefixCheckpoint) error {
	return r.queries.UpdateClassifierThreadPrefix(ctx, sqlc.UpdateClassifierThreadPrefixParams{
		ThreadID: r.threadID, PrefixMessageCount: int32(prefixCheckpoint.MessageCount), PrefixDigest: prefixCheckpoint.Digest,
	})
}

func (r classifierTurnRepo) RootTurnDigest(ctx context.Context) (string, error) {
	root, err := r.queries.GetClassifierThreadRoot(ctx, r.threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return root, err
}

func (r classifierTurnRepo) Get(ctx context.Context, digest string) (router.ClassifierPrediction, bool, error) {
	stored, err := r.queries.GetClassifierPrediction(ctx, sqlc.GetClassifierPredictionParams{ThreadID: r.threadID, TurnDigest: digest})
	return classifierStoredPrediction(stored, err)
}

func (r classifierTurnRepo) PredictionBeforeMessage(ctx context.Context, messageIndex int) (router.ClassifierPrediction, bool, error) {
	stored, err := r.queries.GetClassifierPredictionBeforeMessage(ctx, sqlc.GetClassifierPredictionBeforeMessageParams{ThreadID: r.threadID, MessageIndex: int32(messageIndex)})
	return classifierStoredPrediction(stored, err)
}

func classifierStoredPrediction(stored sqlc.RouterClassifierPrediction, err error) (router.ClassifierPrediction, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return router.ClassifierPrediction{}, false, nil
	}
	if err != nil {
		return router.ClassifierPrediction{}, false, err
	}
	prediction := router.ClassifierPrediction{
		TurnDigest: stored.TurnDigest, RootTurnDigest: stored.RootTurnDigest,
		InputMessageCount:      int(stored.InputMessageCount),
		Features:               router.ClassifierFeatures{UserMessageCount: int(stored.UserMessageCount), ToolCallCount: int(stored.ToolCallCount), ToolErrorCount: int(stored.ToolErrorCount)},
		CompletedResponseCount: int(stored.CompletedResponseCount),
		Complexity:             router.ClassifierComplexity(stored.Complexity), Probabilities: stored.Probabilities,
	}
	return prediction, true, prediction.Validate()
}

func (r classifierTurnRepo) Insert(ctx context.Context, prediction router.ClassifierPrediction) error {
	if err := prediction.Validate(); err != nil {
		return err
	}
	err := r.queries.InsertClassifierPrediction(ctx, sqlc.InsertClassifierPredictionParams{
		ThreadID: r.threadID, TurnDigest: prediction.TurnDigest, RootTurnDigest: prediction.RootTurnDigest,
		InputMessageCount: int32(prediction.InputMessageCount),
		UserMessageCount:  int32(prediction.Features.UserMessageCount), ToolCallCount: int32(prediction.Features.ToolCallCount), ToolErrorCount: int32(prediction.Features.ToolErrorCount),
		CompletedResponseCount: int32(prediction.CompletedResponseCount), Complexity: int16(prediction.Complexity), Probabilities: prediction.Probabilities,
	})
	var constraintError *pgconn.PgError
	if errors.As(err, &constraintError) && constraintError.Code == "23505" {
		return router.ErrClassifierHistoryUnavailable
	}
	return err
}

var _ router.ClassifierSessionStore = (*ClassifierSessionRepo)(nil)
