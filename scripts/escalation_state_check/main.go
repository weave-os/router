// Command escalation_state_check exercises durable classifier leases against a
// migrated disposable database. Set ROUTER_TEST_DATABASE_URL to opt in.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router/escalation"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

func main() {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		slog.Info("ROUTER_TEST_DATABASE_URL unset; skipping escalation database check")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := checkEscalationState(ctx, dsn)
	if err != nil {
		slog.Error("Escalation database check failed", "err", err)
		os.Exit(1)
	}
	slog.Info("Escalation database checks passed: concurrent claims, atomic checkpoints, stale completions, lease expiry, restart, continuation isolation, invalidation, lifetime expiry, fixture cleanup")
}

func checkEscalationState(ctx context.Context, dsn string) (checkErr error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	repositories := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	installation, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "Escalation integration check"})
	if err != nil {
		return err
	}
	store := postgres.NewEscalationRepo(pool)
	activation := sha256.Sum256([]byte(installation.ID))
	scope := sha256.Sum256([]byte(uuid.NewString()))
	fixture := postgres.NewEscalationCheckFixture(pool, *installation, scope)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		checkErr = errors.Join(checkErr, fixture.Cleanup(cleanupCtx))
	}()
	boundary := sha256.Sum256([]byte("first boundary"))
	missedBoundary := sha256.Sum256([]byte("unobserved boundary"))
	type claim struct {
		token    string
		acquired bool
	}
	claims := make([]claim, 8)
	start := make(chan struct{})
	var ready sync.WaitGroup
	var completion errgroup.Group
	for i := range claims {
		ready.Add(1)
		completion.Go(func() error {
			token := uuid.NewString()
			ready.Done()
			<-start
			_, acquired, claimErr := store.Claim(ctx, scope, installation.ID, token, boundary)
			if claimErr != nil {
				return claimErr
			}
			claims[i] = claim{token: token, acquired: acquired}
			return nil
		})
	}
	ready.Wait()
	close(start)
	err = completion.Wait()
	if err != nil {
		return err
	}
	winner := ""
	count := 0
	for _, candidate := range claims {
		if candidate.acquired {
			winner = candidate.token
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("concurrent claims produced %d owners", count)
	}
	session := escalation.Session{Ordinal: 1, FeatureTurns: 1, FeatureState: json.RawMessage(`{"turns":1}`), Floor: escalation.Medium}
	checkpoint := escalation.Checkpoint{Ordinal: 1}
	err = store.Commit(ctx, scope, boundary, uuid.NewString(), session, checkpoint)
	if !errors.Is(err, escalation.ErrLeaseLost) {
		return fmt.Errorf("wrong lease committed: %v", err)
	}
	err = store.Release(ctx, scope, uuid.NewString())
	if err != nil {
		return err
	}
	_, acquired, err := store.Claim(ctx, scope, installation.ID, uuid.NewString(), boundary)
	if err != nil {
		return err
	}
	if acquired {
		return errors.New("wrong-token release removed active lease")
	}
	err = store.Invalidate(ctx, scope, boundary, "")
	if err != nil {
		return err
	}
	var commitRace errgroup.Group
	commitRace.Go(func() error {
		return store.Commit(ctx, scope, boundary, winner, session, checkpoint)
	})
	for range 8 {
		commitRace.Go(func() error {
			return store.Invalidate(ctx, scope, boundary, "")
		})
	}
	if err = commitRace.Wait(); err != nil {
		return err
	}
	// A duplicate may lose its claim, then invalidate after the winner commits.
	err = store.Invalidate(ctx, scope, boundary, "")
	if err != nil {
		return err
	}
	history := json.RawMessage(`[{"role":"assistant","blocks":[{"type":"text","text":"first response"}]}]`)
	err = store.SaveContinuation(ctx, activation, "response-1", scope, 1, history)
	if err != nil {
		return err
	}
	retainedScope, retainedHistory, continuationFound, err := postgres.NewEscalationRepo(pool).Continuation(ctx, activation, "response-1")
	if err != nil {
		return err
	}
	if !continuationFound || retainedScope != scope || !strings.Contains(string(retainedHistory), "first response") {
		return errors.New("restart lost continuation identity/history")
	}
	otherActivation := sha256.Sum256([]byte("other activation"))
	_, _, continuationFound, err = store.Continuation(ctx, otherActivation, "response-1")
	if err != nil {
		return err
	}
	if continuationFound {
		return errors.New("continuation crossed activation boundary")
	}
	saved, found, err := store.Checkpoint(ctx, scope, boundary)
	if err != nil {
		return err
	}
	if !found || saved.Ordinal != 1 {
		return errors.New("committed checkpoint missing")
	}
	err = store.SaveOutcome(ctx, scope, 1, escalation.PreviousOutcome{IsError: true, StatusCode: 503})
	if err != nil {
		return err
	}
	nextToken := uuid.NewString()
	next, acquired, err := postgres.NewEscalationRepo(pool).Claim(ctx, scope, installation.ID, nextToken, boundary)
	if err != nil {
		return err
	}
	if !acquired || next.Ordinal != 1 || next.Floor != escalation.Medium || next.PreviousOutcome == nil || next.PreviousOutcome.StatusCode != 503 || next.FeatureTurns != 1 || len(next.FeatureState) == 0 {
		return errors.New("restart lost committed session/outcome")
	}
	err = store.SaveContinuation(ctx, activation, "response-busy", scope, 1, history)
	if err != nil {
		return err
	}
	_, _, continuationFound, err = store.Continuation(ctx, activation, "response-busy")
	if err != nil {
		return err
	}
	if continuationFound {
		return errors.New("continuation write raced an active observation")
	}
	err = store.SaveOutcome(ctx, scope, 1, escalation.PreviousOutcome{StatusCode: 200})
	if err != nil {
		return err
	}
	next.Ordinal = 2
	next.PreviousOutcome = nil
	err = store.Commit(ctx, scope, boundary, nextToken, next, escalation.Checkpoint{Ordinal: 2})
	if err == nil {
		return errors.New("duplicate boundary committed")
	}
	secondBoundary := sha256.Sum256([]byte("second boundary"))
	err = store.Commit(ctx, scope, secondBoundary, nextToken, next, escalation.Checkpoint{Ordinal: 2})
	if err != nil {
		return fmt.Errorf("duplicate rollback damaged lease/session: %w", err)
	}
	err = store.SaveOutcome(ctx, scope, 1, escalation.PreviousOutcome{IsError: true, StatusCode: 500})
	if err != nil {
		return err
	}
	err = store.SaveContinuation(ctx, activation, "response-stale", scope, 1, history)
	if err != nil {
		return err
	}
	_, _, continuationFound, err = store.Continuation(ctx, activation, "response-stale")
	if err != nil {
		return err
	}
	if continuationFound {
		return errors.New("stale ordinal wrote a continuation")
	}
	expiredToken := uuid.NewString()
	next, acquired, err = store.Claim(ctx, scope, installation.ID, expiredToken, boundary)
	if err != nil {
		return err
	}
	if !acquired || next.Ordinal != 2 || next.PreviousOutcome != nil {
		return errors.New("stale outcome changed a newer observation")
	}
	err = store.Invalidate(ctx, scope, missedBoundary, "")
	if err != nil {
		return err
	}
	timer := time.NewTimer(16 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	replacementToken := uuid.NewString()
	next, acquired, err = store.Claim(ctx, scope, installation.ID, replacementToken, boundary)
	if err != nil {
		return err
	}
	if !acquired || next.FeatureTurns != 0 || string(next.FeatureState) != "null" {
		return errors.New("expired lease did not apply deferred reset when reclaimed")
	}
	next.Ordinal = 3
	thirdBoundary := sha256.Sum256([]byte("third boundary"))
	err = store.Commit(ctx, scope, thirdBoundary, expiredToken, next, escalation.Checkpoint{Ordinal: 3})
	if !errors.Is(err, escalation.ErrLeaseLost) {
		return fmt.Errorf("expired owner committed: %v", err)
	}
	err = store.Commit(ctx, scope, thirdBoundary, replacementToken, next, escalation.Checkpoint{Ordinal: 3})
	if err != nil {
		return err
	}
	fourthBoundary := sha256.Sum256([]byte("fourth boundary"))
	invalidatedToken := uuid.NewString()
	next, acquired, err = store.Claim(ctx, scope, installation.ID, invalidatedToken, fourthBoundary)
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("could not claim invalidation fixture")
	}
	err = store.Invalidate(ctx, scope, missedBoundary, "")
	if err != nil {
		return err
	}
	_, acquired, err = store.Claim(ctx, scope, installation.ID, uuid.NewString(), secondBoundary)
	if err != nil {
		return err
	}
	if acquired {
		return errors.New("invalidation revoked the active owner")
	}
	next.Ordinal = 4
	next.FeatureTurns = 4
	next.FeatureState = json.RawMessage(`{"turns":4}`)
	next.Floor = escalation.High
	err = store.Commit(ctx, scope, fourthBoundary, invalidatedToken, next, escalation.Checkpoint{Ordinal: 4})
	if err != nil {
		return fmt.Errorf("deferred invalidation rejected active owner: %w", err)
	}
	err = store.SaveOutcome(ctx, scope, 4, escalation.PreviousOutcome{IsError: true, StatusCode: 500})
	if err != nil {
		return err
	}
	restartedToken := uuid.NewString()
	restarted, acquired, err := store.Claim(ctx, scope, installation.ID, restartedToken, boundary)
	if err != nil {
		return err
	}
	if !acquired || restarted.Ordinal != 4 || restarted.Floor != escalation.High || restarted.FeatureTurns != 0 || string(restarted.FeatureState) != "null" || restarted.PreviousOutcome != nil {
		return errors.New("deferred reset failed to retain committed ordinal/floor and clear features")
	}
	restarted.Ordinal = 5
	restarted.FeatureTurns = 1
	restarted.FeatureState = json.RawMessage(`{"turns":1}`)
	fifthBoundary := sha256.Sum256([]byte("fifth boundary"))
	err = store.Commit(ctx, scope, fifthBoundary, restartedToken, restarted, escalation.Checkpoint{Ordinal: 5})
	if err != nil {
		return err
	}
	// A released owner must leave the deferred reset for the next claimant.
	releasedToken := uuid.NewString()
	_, acquired, err = store.Claim(ctx, scope, installation.ID, releasedToken, boundary)
	if err != nil || !acquired {
		return fmt.Errorf("claim released-owner fixture: acquired=%t: %w", acquired, err)
	}
	err = store.Invalidate(ctx, scope, missedBoundary, "")
	if err != nil {
		return err
	}
	err = store.Release(ctx, scope, releasedToken)
	if err != nil {
		return err
	}
	restartedToken = uuid.NewString()
	restarted, acquired, err = store.Claim(ctx, scope, installation.ID, restartedToken, boundary)
	if err != nil {
		return err
	}
	if !acquired || restarted.Ordinal != 5 || restarted.FeatureTurns != 0 || string(restarted.FeatureState) != "null" {
		return errors.New("claim did not apply released owner's deferred reset")
	}
	restarted.Ordinal = 6
	restarted.FeatureTurns = 1
	restarted.FeatureState = json.RawMessage(`{"turns":1}`)
	sixthBoundary := sha256.Sum256([]byte("sixth boundary"))
	err = store.Commit(ctx, scope, sixthBoundary, restartedToken, restarted, escalation.Checkpoint{Ordinal: 6})
	if err != nil {
		return err
	}
	// With no owner, invalidation resets immediately and late outcome writes lose.
	err = store.Invalidate(ctx, scope, missedBoundary, "")
	if err != nil {
		return err
	}
	err = store.SaveOutcome(ctx, scope, 6, escalation.PreviousOutcome{IsError: true, StatusCode: 500})
	if err != nil {
		return err
	}
	restartedToken = uuid.NewString()
	restarted, acquired, err = store.Claim(ctx, scope, installation.ID, restartedToken, boundary)
	if err != nil {
		return err
	}
	if !acquired || restarted.Ordinal != 6 || restarted.Floor != escalation.High || restarted.FeatureTurns != 0 || string(restarted.FeatureState) != "null" || restarted.PreviousOutcome != nil {
		return errors.New("idle invalidation did not clear features and preserve ordinal/floor")
	}
	err = store.Release(ctx, scope, restartedToken)
	if err != nil {
		return err
	}
	err = fixture.CheckExpiryBoundary(ctx, boundary)
	if err != nil {
		return err
	}
	freshToken := uuid.NewString()
	fresh, acquired, err := store.Claim(ctx, scope, installation.ID, freshToken, boundary)
	if err != nil {
		return err
	}
	if !acquired || fresh.Ordinal != 0 || fresh.Floor != "" || len(fresh.FeatureState) != 0 {
		return errors.New("claim retained expired lifetime state")
	}
	_, found, err = store.Checkpoint(ctx, scope, boundary)
	if err != nil || found {
		return fmt.Errorf("expired checkpoint survived a new lifetime: found=%t: %w", found, err)
	}
	_, _, found, err = store.Continuation(ctx, activation, "response-1")
	if err != nil || found {
		return fmt.Errorf("expired continuation survived a new lifetime: found=%t: %w", found, err)
	}
	// Leave one child in each table so deferred cleanup verifies its cascades.
	fresh.Ordinal = 1
	fresh.FeatureTurns = 1
	fresh.FeatureState = json.RawMessage(`{"turns":1}`)
	err = store.Commit(ctx, scope, boundary, freshToken, fresh, escalation.Checkpoint{Ordinal: 1})
	if err != nil {
		return err
	}
	err = store.SaveContinuation(ctx, activation, "response-fresh", scope, 1, history)
	if err != nil {
		return err
	}
	failedBoundary := sha256.Sum256([]byte("failed observation boundary"))
	failedToken := uuid.NewString()
	_, acquired, err = store.Claim(ctx, scope, installation.ID, failedToken, failedBoundary)
	if err != nil || !acquired {
		return fmt.Errorf("claim failed observation fixture: acquired=%t: %w", acquired, err)
	}
	// Recovery and the next claimant race; any successful claim must see
	// the reset in the same snapshot as the released lease.
	var recoveryRace errgroup.Group
	recoveryRace.Go(func() error {
		return store.Invalidate(ctx, scope, failedBoundary, failedToken)
	})
	for range 8 {
		recoveryRace.Go(func() error {
			token := uuid.NewString()
			claimed, acquired, claimErr := store.Claim(ctx, scope, installation.ID, token, missedBoundary)
			if claimErr != nil || !acquired {
				return claimErr
			}
			if claimed.FeatureTurns != 0 || string(claimed.FeatureState) != "null" || claimed.PreviousOutcome != nil {
				return errors.New("failed commit exposed a released lease before resetting features")
			}
			return store.Release(ctx, scope, token)
		})
	}
	if err = recoveryRace.Wait(); err != nil {
		return err
	}
	recoveredToken := uuid.NewString()
	recovered, acquired, err := store.Claim(ctx, scope, installation.ID, recoveredToken, missedBoundary)
	if err != nil || !acquired {
		return fmt.Errorf("claim after failed observation recovery: acquired=%t: %w", acquired, err)
	}
	if recovered.FeatureTurns != 0 || string(recovered.FeatureState) != "null" || recovered.PreviousOutcome != nil {
		return errors.New("failed observation recovery retained stale features")
	}
	if err = store.Release(ctx, scope, recoveredToken); err != nil {
		return err
	}
	return store.SweepExpired(ctx)
}
