// Command llm_escalation_state_check verifies durable async judge fencing.
// Run against a migrated disposable database with ROUTER_TEST_DATABASE_URL.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
)

func main() {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		slog.Info("ROUTER_TEST_DATABASE_URL unset; skipping LLM escalation database check")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := check(ctx, dsn); err != nil {
		slog.Error("LLM escalation database check failed", "err", err)
		os.Exit(1)
	}
	slog.Info("LLM escalation database check passed: concurrent deduplication, cadence, stale generations/checkpoints/lifetimes, lease expiry, application, continuations, tenant isolation")
}

func check(ctx context.Context, dsn string) (checkErr error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	repositories := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	installation, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "LLM escalation integration check"})
	if err != nil {
		return err
	}
	scope := sha256.Sum256([]byte(uuid.NewString()))
	cleanup := postgres.NewEscalationCheckFixture(pool, *installation, scope)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		checkErr = errors.Join(checkErr, cleanup.Cleanup(cleanupCtx))
	}()
	fixture, err := postgres.NewLLMEscalationCheckFixture(pool, installation.ID)
	if err != nil {
		return err
	}
	store := postgres.NewLLMEscalationRepo(pool)
	request := llmescalation.StartRequest{Scope: scope, InstallationID: installation.ID, InstructionFingerprint: sha256.Sum256([]byte("initial task")), Config: llmescalation.Config{Mode: llmescalation.ModeActive, Cadence: 3, Digest: "fixture-v1"}}
	session, err := store.Start(ctx, request)
	if err != nil {
		return err
	}
	boundary := sha256.Sum256([]byte("first response"))
	var concurrent errgroup.Group
	var ready sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		ready.Add(1)
		concurrent.Go(func() error {
			ready.Done()
			<-start
			_, err := store.Complete(ctx, llmescalation.CompleteRequest{Session: session, Boundary: boundary, Capacity: true})
			return err
		})
	}
	ready.Wait()
	close(start)
	if err := concurrent.Wait(); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.CompletedTurns != 1 {
		return fmt.Errorf("duplicates counted %d completed turns", session.CompletedTurns)
	}
	finish := func(label string, capacity bool) (llmescalation.Completion, error) {
		return store.Complete(ctx, llmescalation.CompleteRequest{Session: session, Boundary: sha256.Sum256([]byte(label)), RequestID: label, Capacity: capacity})
	}
	if completion, err := finish("second", true); err != nil || completion.Job != nil {
		return fmt.Errorf("unexpected second-turn checkpoint: %w", err)
	}
	checkpoint, err := finish("third", true)
	if err != nil {
		return err
	}
	if checkpoint.Job == nil {
		return errors.New("third completion did not claim judge")
	}
	job := *checkpoint.Job
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending != nil {
		return errors.New("running judge was visible as ready")
	}
	request.InstructionFingerprint = sha256.Sum256([]byte("new instruction"))
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.CompletedTurns != 3 || session.Generation != 2 {
		return errors.New("new instruction reset cadence or failed to advance generation")
	}
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending != nil {
		return errors.New("old instruction judgment became pending")
	}
	stale, found, err := store.GetJob(ctx, installation.ID, job.ID)
	if err != nil {
		return err
	}
	if !found || stale.Status != llmescalation.JobStale {
		return errors.New("old instruction judgment not marked stale")
	}
	for _, label := range []string{"fourth", "fifth"} {
		if _, err := finish(label, true); err != nil {
			return err
		}
	}
	checkpoint, err = finish("sixth", true)
	if err != nil {
		return err
	}
	if checkpoint.Job == nil {
		return errors.New("sixth completion did not claim judge")
	}
	job = *checkpoint.Job
	if err := fixture.ExpireJob(ctx, job.ID); err != nil {
		return err
	}
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending != nil {
		return errors.New("expired judge lease became pending")
	}
	for _, label := range []string{"seventh", "eighth"} {
		if _, err := finish(label, true); err != nil {
			return err
		}
	}
	checkpoint, err = finish("ninth", true)
	if err != nil {
		return err
	}
	if checkpoint.Job == nil {
		return errors.New("ninth completion did not claim judge")
	}
	job = *checkpoint.Job
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending == nil {
		return errors.New("fresh positive judgment unavailable")
	}
	if applied, err := store.Apply(ctx, llmescalation.ApplyRequest{Session: session, JobID: job.ID, Floor: escalation.Maximum, RequestID: "apply-request", Turn: 10}); err != nil || !applied {
		return fmt.Errorf("valid floor was not applied: %w", err)
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Floor != escalation.Maximum || session.Pending != nil {
		return errors.New("floor/application persistence failed")
	}
	appliedJob, _, err := store.GetJob(ctx, installation.ID, job.ID)
	if err != nil {
		return err
	}
	if appliedJob.AppliedRequestID != "apply-request" || appliedJob.AppliedTurn == nil || *appliedJob.AppliedTurn != 10 {
		return errors.New("application attribution missing")
	}
	request.InstructionFingerprint = sha256.Sum256([]byte("yet another instruction"))
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Floor != escalation.Maximum {
		return errors.New("new instruction erased accepted floor")
	}
	activation := sha256.Sum256([]byte(uuid.NewString()))
	if err := store.SaveContinuation(ctx, llmescalation.ContinuationRequest{Session: session, Activation: activation, ResponseID: "response-id", History: json.RawMessage(`[]`)}); err != nil {
		return err
	}
	if _, found, err := store.Continuation(ctx, activation, "response-id"); err != nil || !found {
		return fmt.Errorf("continuation missing: %w", err)
	}
	if _, found, err := store.Continuation(ctx, sha256.Sum256([]byte("other-key")), "response-id"); err != nil || found {
		return fmt.Errorf("continuation isolation failed: %w", err)
	}
	if _, found, err := store.GetJob(ctx, uuid.NewString(), job.ID); err != nil || found {
		return fmt.Errorf("job tenant isolation failed: %w", err)
	}
	oldLifetime := session.Lifetime
	if err := fixture.ExpireSession(ctx, oldLifetime); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Lifetime == oldLifetime || session.Floor != "" || session.CompletedTurns != 0 {
		return errors.New("expired lifetime resurrected")
	}
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	if _, found, err := store.Continuation(ctx, activation, "response-id"); err != nil || found {
		return fmt.Errorf("expired continuation survived: %w", err)
	}
	for _, label := range []string{"new-first", "new-second"} {
		if _, err := finish(label, true); err != nil {
			return err
		}
	}
	checkpoint, err = finish("new-third", true)
	if err != nil {
		return err
	}
	if checkpoint.Job == nil {
		return errors.New("recreated lifetime missing checkpoint")
	}
	job = *checkpoint.Job
	for _, label := range []string{"new-fourth", "new-fifth"} {
		if _, err := finish(label, true); err != nil {
			return err
		}
	}
	checkpoint, err = finish("new-sixth", true)
	if err != nil {
		return err
	}
	if checkpoint.Job != nil {
		return errors.New("overlapping paid judge was admitted")
	}
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending != nil {
		return errors.New("superseded checkpoint was applicable")
	}
	return nil
}
