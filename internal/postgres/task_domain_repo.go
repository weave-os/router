package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/semaphore"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/router/taskdomain"
	"weave-os/router/internal/sqlc"
)

// ErrTaskProfileCapacity reports that every inference transaction slot stayed busy for the
// bounded wait; it is capacity pressure, not a classifier or request timeout.
var ErrTaskProfileCapacity = errors.New("task profile transaction capacity reached")

// slotWait bounds how long a first turn waits for an inference transaction slot, leaving
// most of the request's taskdomain.Timeout for classification itself.
const slotWait = time.Second

// TaskDomainRepo bounds connections held by optional first-turn inference.
type TaskDomainRepo struct {
	pool         *pgxpool.Pool
	transactions *semaphore.Weighted
}

// NewTaskDomainRepo requires the writable router database.
func NewTaskDomainRepo(pool *pgxpool.Pool) *TaskDomainRepo {
	return &TaskDomainRepo{pool: pool, transactions: semaphore.NewWeighted(2)}
}

// Resolve commits one terminal profile under a row lock shared by all workers.
func (r *TaskDomainRepo) Resolve(ctx context.Context, key taskdomain.Key, resume bool, classify func(context.Context) taskdomain.Outcome) (taskdomain.Outcome, error) {
	if !taskdomain.ValidDigest(key.Conversation) || !taskdomain.ValidDigest(key.Release) || !taskdomain.ValidDigest(key.Evidence) || (!resume && !taskdomain.ValidDigest(key.Root)) {
		return taskdomain.Outcome{}, errors.New("invalid task profile identity")
	}
	if resume {
		profiles, err := dbbudget.Queries(r.pool).GetTaskDomainResumeProfiles(ctx, sqlc.GetTaskDomainResumeProfilesParams{ConversationKey: key.Conversation, ReleaseSha256: key.Release, EvidenceSha256: key.Evidence})
		if err != nil {
			return taskdomain.Outcome{}, err
		}
		if len(profiles) != 1 {
			return taskdomain.Outcome{Status: taskdomain.NoTask, ReleaseSHA256: key.Release, EvidenceSHA256: key.Evidence}, nil
		}
		return decodeTaskProfile(profiles[0], key)
	}
	stored, err := dbbudget.Queries(r.pool).GetTaskDomainProfile(ctx, sqlc.GetTaskDomainProfileParams{ConversationKey: key.Conversation, RootSha256: key.Root, ReleaseSha256: key.Release, EvidenceSha256: key.Evidence})
	if err == nil {
		return decodeTaskProfile(stored, key)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return taskdomain.Outcome{}, err
	}
	// Wait briefly for a slot: one that frees moments later is used, but a long wait would
	// leave classification too little budget and persist a timeout for this task.
	slotCtx, cancelSlot := context.WithTimeout(ctx, slotWait)
	err = r.transactions.Acquire(slotCtx, 1)
	cancelSlot()
	if err != nil {
		if ctx.Err() != nil {
			return taskdomain.Outcome{}, ctx.Err()
		}
		return taskdomain.Outcome{}, ErrTaskProfileCapacity
	}
	defer r.transactions.Release(1)
	var outcome taskdomain.Outcome
	err = pgx.BeginTxFunc(ctx, dbbudget.NewDBTX(r.pool), pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		queries := dbbudget.Queries(tx)
		err := queries.InsertTaskDomainProfile(ctx, sqlc.InsertTaskDomainProfileParams{ConversationKey: key.Conversation, RootSha256: key.Root, ReleaseSha256: key.Release, EvidenceSha256: key.Evidence})
		if err != nil {
			return err
		}
		stored, err := queries.GetTaskDomainProfileForUpdate(ctx, sqlc.GetTaskDomainProfileForUpdateParams{ConversationKey: key.Conversation, RootSha256: key.Root, ReleaseSha256: key.Release, EvidenceSha256: key.Evidence})
		if err != nil {
			return err
		}
		if len(stored) > 0 {
			outcome, err = decodeTaskProfile(stored, key)
			return err
		}
		outcome = classify(ctx)
		encoded, err := json.Marshal(outcome)
		if err != nil {
			return err
		}
		if _, err := decodeTaskProfile(encoded, key); err != nil {
			return err
		}
		return queries.UpdateTaskDomainProfile(ctx, sqlc.UpdateTaskDomainProfileParams{Outcome: encoded, Failed: outcome.Status != taskdomain.Ready, ConversationKey: key.Conversation, RootSha256: key.Root, ReleaseSha256: key.Release, EvidenceSha256: key.Evidence})
	})
	return outcome, err
}

func decodeTaskProfile(encoded []byte, key taskdomain.Key) (taskdomain.Outcome, error) {
	var outcome taskdomain.Outcome
	if err := json.Unmarshal(encoded, &outcome); err != nil {
		return outcome, errors.New("invalid stored task profile")
	}
	if outcome.ReleaseSHA256 != key.Release || outcome.EvidenceSHA256 != key.Evidence {
		return outcome, errors.New("stored task profile version mismatch")
	}
	switch outcome.Status {
	case taskdomain.Ready:
		if err := outcome.Profile.Validate(); err != nil {
			return taskdomain.Outcome{}, err
		}
	case taskdomain.Unavailable, taskdomain.TimedOut:
		if outcome.Profile != nil {
			return taskdomain.Outcome{}, errors.New("failed task profile contains prediction")
		}
	default:
		return taskdomain.Outcome{}, errors.New("invalid stored task profile status")
	}
	outcome.Cached = true
	return outcome, nil
}

// Sweep removes expired content-free profiles during the normal worker sweep.
func (r *TaskDomainRepo) Sweep(ctx context.Context) error {
	return dbbudget.Queries(r.pool).DeleteExpiredTaskDomainProfiles(ctx)
}
