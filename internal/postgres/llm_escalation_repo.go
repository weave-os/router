package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/sqlc"
)

// LLMEscalationRepo persists short claims separately from XGBoost observations.
type LLMEscalationRepo struct{ pool *pgxpool.Pool }

// NewLLMEscalationRepo wires the shared pool without owning its lifecycle.
func NewLLMEscalationRepo(pool *pgxpool.Pool) *LLMEscalationRepo {
	return &LLMEscalationRepo{pool: pool}
}

var _ llmescalation.Store = (*LLMEscalationRepo)(nil)

// Start updates instruction freshness before returning any consumable verdict.
func (r *LLMEscalationRepo) Start(ctx context.Context, request llmescalation.StartRequest) (llmescalation.Session, error) {
	installationID, err := uuid.Parse(request.InstallationID)
	if err != nil {
		return llmescalation.Session{}, fmt.Errorf("parse escalation installation: %w", err)
	}
	if request.Config.Cadence < 3 || request.Config.Cadence > 5 {
		return llmescalation.Session{}, errors.New("escalation cadence must be 3, 4, or 5")
	}
	session := llmescalation.Session{Scope: request.Scope, Lifetime: uuid.NewString(), InstallationID: request.InstallationID, Config: request.Config, InstructionFingerprint: request.InstructionFingerprint, Generation: 1}
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		if err := queries.DeleteExpiredLLMEscalationScope(ctx, request.Scope[:]); err != nil {
			return err
		}
		encoded, err := json.Marshal(session)
		if err != nil {
			return err
		}
		if err := queries.InsertLLMEscalationSession(ctx, sqlc.InsertLLMEscalationSessionParams{Scope: request.Scope[:], Lifetime: uuid.MustParse(session.Lifetime), InstallationID: installationID, State: encoded}); err != nil {
			return err
		}
		session, err = lockedLLMSession(ctx, queries, request.Scope)
		if err != nil {
			return err
		}
		if session.InstallationID != request.InstallationID || session.Config != request.Config {
			return errors.New("escalation scope configuration mismatch")
		}
		if session.InstructionFingerprint != request.InstructionFingerprint {
			session.InstructionFingerprint = request.InstructionFingerprint
			session.Generation++
		}
		if err := saveLLMSession(ctx, queries, session); err != nil {
			return err
		}
		job, found, err := currentLLMJob(ctx, queries, session)
		if err != nil {
			return err
		}
		if found && job.Status == llmescalation.JobCompleted && job.Judgment != nil && job.Judgment.Escalate {
			session.Pending = &job
		}
		return nil
	})
	return session, err
}

// Complete deduplicates a successful response and atomically claims each cadence boundary.
func (r *LLMEscalationRepo) Complete(ctx context.Context, request llmescalation.CompleteRequest) (llmescalation.Completion, error) {
	completion := llmescalation.Completion{}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		session, err := lockedLLMSession(ctx, queries, request.Session.Scope)
		if err != nil {
			return err
		}
		if session.Lifetime != request.Session.Lifetime || session.Generation != request.Session.Generation {
			return llmescalation.ErrStale
		}
		lifetime, err := uuid.Parse(session.Lifetime)
		if err != nil {
			return err
		}
		inserted, err := queries.InsertLLMEscalationCompletion(ctx, sqlc.InsertLLMEscalationCompletionParams{Lifetime: lifetime, Boundary: request.Boundary[:]})
		if err != nil {
			return err
		}
		completion.Session = session
		if inserted == 0 {
			completion.Duplicate = true
			return nil
		}
		session.CompletedTurns++
		if session.CompletedTurns%int64(session.Config.Cadence) == 0 && session.Floor == "" {
			session.LatestCheckpoint = session.CompletedTurns
			job := llmescalation.Job{
				ID: uuid.NewString(), Scope: session.Scope, Lifetime: session.Lifetime,
				Generation: session.Generation, Checkpoint: session.LatestCheckpoint,
				RequestID: request.RequestID, Status: llmescalation.JobRunning, CreatedAt: time.Now().UTC(),
				Model: policy.EscalationJudgeModel, Provider: providers.ProviderFireworks,
				Version: llmescalation.Version, PromptRevision: llmescalation.SwitchyardRevision,
				SchemaRevision: llmescalation.SwitchyardRevision, RendererRevision: llmescalation.SwitchyardRevision,
				ConfigDigest: session.Config.Digest,
			}
			_, liveErr := queries.GetLLMEscalationLiveJob(ctx, sqlc.GetLLMEscalationLiveJobParams{Lifetime: lifetime, Status: string(llmescalation.JobRunning)})
			if liveErr != nil && !errors.Is(liveErr, sql.ErrNoRows) {
				return liveErr
			}
			switch {
			case session.Generation != request.Session.Generation:
				job.Failure = llmescalation.FailureStale
			case session.JudgeCalls >= llmescalation.MaxJudgeCalls:
				job.Failure = llmescalation.FailureCallLimit
			case liveErr == nil:
				job.Failure = llmescalation.FailureInFlight
			case !request.Capacity:
				job.Failure = llmescalation.FailureCapacity
			}
			if job.Failure != llmescalation.FailureNone {
				job.Status = llmescalation.JobSkipped
				job.FinishedAt = &job.CreatedAt
			} else {
				session.JudgeCalls++
				completion.Job = &job
			}
			encoded, err := json.Marshal(job)
			if err != nil {
				return err
			}
			if err := queries.InsertLLMEscalationJob(ctx, sqlc.InsertLLMEscalationJobParams{ID: uuid.MustParse(job.ID), Lifetime: lifetime, Generation: job.Generation, Checkpoint: job.Checkpoint, Status: string(job.Status), Job: encoded}); err != nil {
				return err
			}
		}
		completion.Session = session
		return saveLLMSession(ctx, queries, session)
	})
	return completion, err
}

// FinishJob writes only the job verdict; later turns' state is never overwritten.
func (r *LLMEscalationRepo) FinishJob(ctx context.Context, job llmescalation.Job, judgment llmescalation.Judgment, failure llmescalation.FailureCode) error {
	lifetime, err := uuid.Parse(job.Lifetime)
	if err != nil {
		return err
	}
	jobID, err := uuid.Parse(job.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	job.FinishedAt = &now
	job.Judgment = &judgment
	job.Failure = failure
	job.Status = llmescalation.JobCompleted
	if failure != llmescalation.FailureNone {
		job.Status = llmescalation.JobFailed
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		_, err := lockedLLMSession(ctx, queries, job.Scope)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(job)
		if err != nil {
			return err
		}
		changed, err := queries.UpdateLLMEscalationJobFinished(ctx, sqlc.UpdateLLMEscalationJobFinishedParams{ID: jobID, Lifetime: lifetime, RunningStatus: string(llmescalation.JobRunning), Status: string(job.Status), Job: encoded})
		if err != nil {
			return err
		}
		if changed != 0 {
			return nil
		}
		job.Status = llmescalation.JobStale
		job.Failure = llmescalation.FailureStale
		encoded, err = json.Marshal(job)
		if err != nil {
			return err
		}
		return queries.UpdateLLMEscalationJobStale(ctx, sqlc.UpdateLLMEscalationJobStaleParams{ID: jobID, Lifetime: lifetime, RunningStatus: string(llmescalation.JobRunning), Status: string(job.Status), Job: encoded})
	})
}

// Apply latches a floor only for a still-current positive verdict.
func (r *LLMEscalationRepo) Apply(ctx context.Context, request llmescalation.ApplyRequest) (bool, error) {
	applied := false
	if escalation.Rank(request.Floor) < 0 {
		return false, errors.New("invalid escalation floor")
	}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		session, err := lockedLLMSession(ctx, queries, request.Session.Scope)
		if err != nil {
			return err
		}
		if session.Lifetime != request.Session.Lifetime || session.Generation != request.Session.Generation || (session.Config.Mode != llmescalation.ModeActive && session.Config.Mode != llmescalation.ModeShadow) {
			return nil
		}
		if escalation.Rank(session.Floor) >= escalation.Rank(request.Floor) {
			applied = true
			return nil
		}
		job, found, err := currentLLMJob(ctx, queries, session)
		if err != nil {
			return err
		}
		if !found || job.ID != request.JobID || job.Status != llmescalation.JobCompleted || job.Judgment == nil || !job.Judgment.Escalate {
			return nil
		}
		session.Floor = escalation.Higher(session.Floor, request.Floor)
		if err := saveLLMSession(ctx, queries, session); err != nil {
			return err
		}
		job.Status = llmescalation.JobApplied
		job.AppliedRequestID = request.RequestID
		job.AppliedTurn = &request.Turn
		encoded, err := json.Marshal(job)
		if err != nil {
			return err
		}
		changed, err := queries.UpdateLLMEscalationJobApplied(ctx, sqlc.UpdateLLMEscalationJobAppliedParams{ID: uuid.MustParse(job.ID), Lifetime: uuid.MustParse(job.Lifetime), CompletedStatus: string(llmescalation.JobCompleted), Status: string(job.Status), Job: encoded})
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("escalation verdict changed during apply")
		}
		applied = true
		return nil
	})
	return applied, err
}

// RecordNoTarget annotates a fresh positive verdict while leaving it available
// for another eligible request until normal checkpoint or instruction expiry.
func (r *LLMEscalationRepo) RecordNoTarget(ctx context.Context, request llmescalation.ApplyRequest) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := sqlc.New(tx)
		session, err := lockedLLMSession(ctx, queries, request.Session.Scope)
		if err != nil {
			return err
		}
		if session.Lifetime != request.Session.Lifetime || session.Generation != request.Session.Generation {
			return nil
		}
		job, found, err := currentLLMJob(ctx, queries, session)
		if err != nil {
			return err
		}
		if !found || job.ID != request.JobID || job.Status != llmescalation.JobCompleted || job.Judgment == nil || !job.Judgment.Escalate {
			return nil
		}
		job.Failure = llmescalation.FailureNoTarget
		encoded, err := json.Marshal(job)
		if err != nil {
			return err
		}
		_, err = queries.UpdateLLMEscalationJobMetadata(ctx, sqlc.UpdateLLMEscalationJobMetadataParams{
			ID: uuid.MustParse(job.ID), Lifetime: uuid.MustParse(job.Lifetime),
			CompletedStatus: string(llmescalation.JobCompleted), Job: encoded,
		})
		return err
	})
}

func lockedLLMSession(ctx context.Context, queries *sqlc.Queries, scope [32]byte) (llmescalation.Session, error) {
	encoded, err := queries.GetLLMEscalationSessionLocked(ctx, scope[:])
	if err != nil {
		return llmescalation.Session{}, err
	}
	var session llmescalation.Session
	err = json.Unmarshal(encoded, &session)
	return session, err
}
func saveLLMSession(ctx context.Context, queries *sqlc.Queries, session llmescalation.Session) error {
	session.Pending = nil
	session.LastActivityAt = time.Now().UTC()
	encoded, err := json.Marshal(session)
	if err != nil {
		return err
	}
	lifetime, err := uuid.Parse(session.Lifetime)
	if err != nil {
		return err
	}
	changed, err := queries.UpdateLLMEscalationSession(ctx, sqlc.UpdateLLMEscalationSessionParams{Scope: session.Scope[:], Lifetime: lifetime, State: encoded})
	if err != nil {
		return err
	}
	if changed != 1 {
		return llmescalation.ErrStale
	}
	return nil
}
func currentLLMJob(ctx context.Context, queries *sqlc.Queries, session llmescalation.Session) (llmescalation.Job, bool, error) {
	lifetime, err := uuid.Parse(session.Lifetime)
	if err != nil {
		return llmescalation.Job{}, false, err
	}
	encoded, err := queries.GetLLMEscalationCheckpointJob(ctx, sqlc.GetLLMEscalationCheckpointJobParams{Lifetime: lifetime, Generation: session.Generation, Checkpoint: session.LatestCheckpoint})
	if errors.Is(err, sql.ErrNoRows) {
		return llmescalation.Job{}, false, nil
	}
	if err != nil {
		return llmescalation.Job{}, false, err
	}
	var job llmescalation.Job
	err = json.Unmarshal(encoded, &job)
	return job, err == nil, err
}

// SaveContinuation persists only bounded immutable protocol recovery state.
func (r *LLMEscalationRepo) SaveContinuation(ctx context.Context, request llmescalation.ContinuationRequest) error {
	if request.ResponseID == "" || len(request.History) > 1024*1024 {
		return errors.New("invalid escalation continuation")
	}
	lifetime, err := uuid.Parse(request.Session.Lifetime)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(request.ResponseID))
	return sqlc.New(r.pool).InsertLLMEscalationContinuation(ctx, sqlc.InsertLLMEscalationContinuationParams{Activation: request.Activation[:], ResponseDigest: digest[:], Lifetime: lifetime, History: request.History})
}

// Continuation resolves history within the activation's installation/key boundary.
func (r *LLMEscalationRepo) Continuation(ctx context.Context, activation [32]byte, responseID string) (llmescalation.Continuation, bool, error) {
	digest := sha256.Sum256([]byte(responseID))
	row, err := sqlc.New(r.pool).GetLLMEscalationContinuation(ctx, sqlc.GetLLMEscalationContinuationParams{Activation: activation[:], ResponseDigest: digest[:]})
	if errors.Is(err, sql.ErrNoRows) {
		return llmescalation.Continuation{}, false, nil
	}
	if err != nil {
		return llmescalation.Continuation{}, false, err
	}
	continuation := llmescalation.Continuation{History: row.History}
	copy(continuation.Scope[:], row.Scope)
	return continuation, true, nil
}

// ListJobs returns recent installation-owned checkpoint metadata.
func (r *LLMEscalationRepo) ListJobs(ctx context.Context, installation string, limit int) ([]llmescalation.Job, error) {
	id, err := uuid.Parse(installation)
	if err != nil {
		return nil, err
	}
	rows, err := sqlc.New(r.pool).GetLLMEscalationJobs(ctx, sqlc.GetLLMEscalationJobsParams{InstallationID: id, PageLimit: int32(min(max(limit, 1), 500))})
	if err != nil {
		return nil, err
	}
	return decodeLLMJobs(rows)
}

// GetJob checks installation ownership before returning a checkpoint.
func (r *LLMEscalationRepo) GetJob(ctx context.Context, installation, jobID string) (llmescalation.Job, bool, error) {
	id, err := uuid.Parse(installation)
	if err != nil {
		return llmescalation.Job{}, false, err
	}
	checkpointID, err := uuid.Parse(jobID)
	if err != nil {
		return llmescalation.Job{}, false, err
	}
	encoded, err := sqlc.New(r.pool).GetLLMEscalationJob(ctx, sqlc.GetLLMEscalationJobParams{InstallationID: id, ID: checkpointID})
	if errors.Is(err, sql.ErrNoRows) {
		return llmescalation.Job{}, false, nil
	}
	if err != nil {
		return llmescalation.Job{}, false, err
	}
	var job llmescalation.Job
	err = json.Unmarshal(encoded, &job)
	return job, err == nil, err
}

// ListSessions returns bounded pages of installation-owned sessions.
func (r *LLMEscalationRepo) ListSessions(ctx context.Context, installation string, limit, offset int32) ([]llmescalation.Session, error) {
	id := uuid.Nil
	if installation != "" {
		var err error
		id, err = uuid.Parse(installation)
		if err != nil {
			return nil, err
		}
	}
	pageOffset := offset
	if pageOffset < 0 {
		pageOffset = 0
	}
	pageLimit := limit
	if pageLimit < 1 {
		pageLimit = 1
	}
	if pageLimit > 201 {
		pageLimit = 201
	}
	rows, err := sqlc.New(r.pool).GetLLMEscalationSessions(ctx, sqlc.GetLLMEscalationSessionsParams{AllInstallations: installation == "", InstallationID: id, PageLimit: pageLimit, PageOffset: pageOffset})
	if err != nil {
		return nil, err
	}
	sessions := make([]llmescalation.Session, 0, len(rows))
	for _, encoded := range rows {
		var session llmescalation.Session
		if err := json.Unmarshal(encoded, &session); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// Summary returns content-free counters across retained sessions.
func (r *LLMEscalationRepo) Summary(ctx context.Context, installation string) (llmescalation.Summary, error) {
	id := uuid.Nil
	if installation != "" {
		var err error
		id, err = uuid.Parse(installation)
		if err != nil {
			return llmescalation.Summary{}, err
		}
	}
	row, err := sqlc.New(r.pool).GetLLMEscalationSummary(ctx, sqlc.GetLLMEscalationSummaryParams{AllInstallations: installation == "", InstallationID: id})
	if err != nil {
		return llmescalation.Summary{}, err
	}
	return llmescalation.Summary{
		PositiveJudgments: row.PositiveJudgments, ActualInterventions: row.ActualInterventions,
		ShadowInterventions: row.ShadowInterventions, StaleResults: row.StaleResults,
		Timeouts: row.Timeouts, InvalidResponses: row.InvalidResponses,
		CapacitySkips: row.CapacitySkips, AttemptLimitExhaustion: row.AttemptLimitExhaustion,
	}, nil
}

// GetSession resolves a scope only within the requested installation.
func (r *LLMEscalationRepo) GetSession(ctx context.Context, installation string, scope [32]byte) (llmescalation.Session, []llmescalation.Job, bool, error) {
	id, err := uuid.Parse(installation)
	if err != nil {
		return llmescalation.Session{}, nil, false, err
	}
	encoded, err := sqlc.New(r.pool).GetLLMEscalationSessionDetail(ctx, sqlc.GetLLMEscalationSessionDetailParams{InstallationID: id, Scope: scope[:]})
	if errors.Is(err, sql.ErrNoRows) {
		return llmescalation.Session{}, nil, false, nil
	}
	if err != nil {
		return llmescalation.Session{}, nil, false, err
	}
	var session llmescalation.Session
	if err := json.Unmarshal(encoded, &session); err != nil {
		return session, nil, false, err
	}
	lifetime, err := uuid.Parse(session.Lifetime)
	if err != nil {
		return session, nil, false, err
	}
	rows, err := sqlc.New(r.pool).GetLLMEscalationSessionJobs(ctx, lifetime)
	if err != nil {
		return session, nil, false, err
	}
	jobs, err := decodeLLMJobs(rows)
	return session, jobs, err == nil, err
}
func decodeLLMJobs(rows [][]byte) ([]llmescalation.Job, error) {
	jobs := make([]llmescalation.Job, 0, len(rows))
	for _, encoded := range rows {
		var job llmescalation.Job
		if err := json.Unmarshal(encoded, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// SweepExpired deletes operational state and all attached histories together.
func (r *LLMEscalationRepo) SweepExpired(ctx context.Context) error {
	queries := sqlc.New(r.pool)
	if err := queries.ExpireLLMEscalationJobLeases(ctx); err != nil {
		return err
	}
	return queries.DeleteExpiredLLMEscalationSessions(ctx)
}
