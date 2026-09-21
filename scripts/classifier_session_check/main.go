// Command classifier_session_check verifies locking and persistence on a migrated
// disposable database. Set ROUTER_TEST_DATABASE_URL; it never reads production config.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

func main() {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		slog.Error("ROUTER_TEST_DATABASE_URL must name a disposable migrated database")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := check(ctx, dsn); err != nil {
		slog.Error("Classifier session database check failed", "err", err)
		os.Exit(1)
	}
	slog.Info("Classifier database check passed: bounded transaction capacity, idempotent enrollment, concurrent commit, replica recovery, rollback, divergence, credential/release/expiry fencing and cascading cleanup")
}

func check(ctx context.Context, dsn string) (checkErr error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return err
	}
	config.MaxConns = 6
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return err
	}
	defer pool.Close()
	repository := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	installation, err := repository.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "Classifier integration fixture"})
	if err != nil {
		return err
	}
	credential := sha256.Sum256([]byte(uuid.NewString()))
	cleanup := postgres.NewEscalationCheckFixture(pool, *installation, credential)
	cleaned := false
	defer func() {
		if !cleaned {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			checkErr = errors.Join(checkErr, cleanup.Cleanup(cleanupCtx))
		}
	}()
	store := postgres.NewClassifierSessionRepo(pool)
	thread, err := store.Create(ctx, router.ClassifierThread{InstallationID: uuid.MustParse(installation.ID), CredentialSHA256: credential, RequestID: uuid.New(), ThreadID: uuid.New(), Release: "llm-classifier-v1.0.0", ReleaseSHA256: strings.Repeat("a", 64), SelectionPolicySHA256: strings.Repeat("b", 64), ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second)})
	if err != nil {
		return err
	}
	retry := thread
	retry.ThreadID, retry.Release = uuid.New(), "llm-classifier-v2.0.0"
	retry, err = store.Create(ctx, retry)
	if err != nil || retry != thread {
		return fmt.Errorf("handshake retry rebound original thread: %w", err)
	}
	prediction := router.ClassifierPrediction{TurnDigest: strings.Repeat("b", 64), RootTurnDigest: strings.Repeat("b", 64), Features: router.ClassifierFeatures{UserMessageCount: 1}, Complexity: router.ClassifierMedium, Probabilities: []float64{0.1, 0.7, 0.1, 0.1}}
	var inferenceCalls atomic.Int32
	var concurrent errgroup.Group
	concurrent.SetLimit(2)
	for range 8 {
		concurrent.Go(func() error {
			return store.WithThread(ctx, thread, func(turns router.ClassifierTurnStore) error {
				_, found, err := turns.Get(ctx, prediction.TurnDigest)
				if err != nil || found {
					return err
				}
				inferenceCalls.Add(1)
				return turns.Insert(ctx, prediction)
			})
		})
	}
	if err := concurrent.Wait(); err != nil {
		return err
	}
	if inferenceCalls.Load() != 1 {
		return fmt.Errorf("overlapping replicas classified %d times", inferenceCalls.Load())
	}
	if err := checkClassifierCapacity(ctx, pool, store, thread); err != nil {
		return err
	}
	replica := postgres.NewClassifierSessionRepo(pool)
	err = replica.WithThread(ctx, thread, func(turns router.ClassifierTurnStore) error {
		stored, found, err := turns.Get(ctx, prediction.TurnDigest)
		if err != nil || !found || !reflect.DeepEqual(stored, prediction) {
			return fmt.Errorf("committed prediction did not survive replica replacement: %w", err)
		}
		rootDigest, err := turns.RootTurnDigest(ctx)
		if err != nil || rootDigest != prediction.RootTurnDigest {
			return fmt.Errorf("root identity mismatch: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	divergent := prediction
	divergent.TurnDigest = strings.Repeat("c", 64)
	err = store.WithThread(ctx, thread, func(turns router.ClassifierTurnStore) error { return turns.Insert(ctx, divergent) })
	if !errors.Is(err, router.ErrClassifierHistoryUnavailable) {
		return fmt.Errorf("divergent ordinal was not rejected: %v", err)
	}
	divergent.Features.UserMessageCount = 2
	abort := errors.New("fixture abort before commit")
	err = store.WithThread(ctx, thread, func(turns router.ClassifierTurnStore) error {
		if err := turns.Insert(ctx, divergent); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		return fmt.Errorf("rollback did not propagate failure: %v", err)
	}
	err = replica.WithThread(ctx, thread, func(turns router.ClassifierTurnStore) error {
		_, found, err := turns.Get(ctx, divergent.TurnDigest)
		if found {
			return errors.New("rolled back prediction persisted")
		}
		return err
	})
	if err != nil {
		return err
	}
	for _, mutate := range []func(*router.ClassifierThread){
		func(thread *router.ClassifierThread) { thread.CredentialSHA256 = sha256.Sum256([]byte("foreign")) },
		func(thread *router.ClassifierThread) { thread.InstallationID = uuid.New() },
		func(thread *router.ClassifierThread) { thread.Release = "llm-classifier-v2.0.0" },
		func(thread *router.ClassifierThread) { thread.ReleaseSHA256 = strings.Repeat("d", 64) },
		func(thread *router.ClassifierThread) { thread.SelectionPolicySHA256 = strings.Repeat("d", 64) },
	} {
		foreign := thread
		mutate(&foreign)
		err := replica.WithThread(ctx, foreign, func(router.ClassifierTurnStore) error { return errors.New("foreign scope entered callback") })
		if !errors.Is(err, router.ErrClassifierThreadInvalid) {
			return fmt.Errorf("scope fencing failed: %v", err)
		}
	}
	expired := thread
	expired.ThreadID, expired.RequestID, expired.ExpiresAt = uuid.New(), uuid.New(), time.Now().Add(-time.Hour)
	expired, err = store.Create(ctx, expired)
	if err != nil {
		return err
	}
	err = store.WithThread(ctx, expired, func(router.ClassifierTurnStore) error { return errors.New("expired scope entered callback") })
	if !errors.Is(err, router.ErrClassifierThreadInvalid) {
		return fmt.Errorf("expiry fencing failed: %v", err)
	}
	if err := cleanup.Cleanup(ctx); err != nil {
		return err
	}
	cleaned = true
	err = store.WithThread(ctx, thread, func(router.ClassifierTurnStore) error { return errors.New("deleted installation entered callback") })
	if !errors.Is(err, router.ErrClassifierThreadInvalid) {
		return fmt.Errorf("deleted installation retained classifier thread: %v", err)
	}
	return nil
}

func checkClassifierCapacity(ctx context.Context, pool *pgxpool.Pool, store *postgres.ClassifierSessionRepo, original router.ClassifierThread) error {
	for _, cancelTransactions := range []bool{false, true, false} {
		err := func() error {
			transactionCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			release := make(chan struct{}, 2)
			defer close(release)
			entered := make(chan struct{}, 2)
			var active errgroup.Group
			for range 2 {
				thread := original
				thread.ThreadID, thread.RequestID = uuid.New(), uuid.New()
				thread, err := store.Create(ctx, thread)
				if err != nil {
					return err
				}
				active.Go(func() error {
					return store.WithThread(transactionCtx, thread, func(router.ClassifierTurnStore) error {
						entered <- struct{}{}
						select {
						case <-release:
							return nil
						case <-transactionCtx.Done():
							return transactionCtx.Err()
						}
					})
				})
			}
			for range 2 {
				select {
				case <-entered:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			var reserved []*pgxpool.Conn
			defer func() {
				for _, connection := range reserved {
					connection.Release()
				}
			}()
			availableCtx, availableCancel := context.WithTimeout(ctx, time.Second)
			defer availableCancel()
			for range 4 {
				connection, err := pool.Acquire(availableCtx)
				if err != nil {
					return fmt.Errorf("classifier work consumed unrelated connection capacity: %w", err)
				}
				reserved = append(reserved, connection)
			}
			rejectCtx, rejectCancel := context.WithTimeout(ctx, 250*time.Millisecond)
			defer rejectCancel()
			err := store.WithThread(rejectCtx, original, func(router.ClassifierTurnStore) error {
				return errors.New("saturated classifier reached inference")
			})
			status, classified := proxy.ClassifyDispatchError(err)
			if !errors.Is(err, router.ErrClassifierUnavailable) || !classified || status.Status != http.StatusServiceUnavailable {
				return fmt.Errorf("saturated classifier did not reject before pool acquisition with 503: %v", err)
			}
			if cancelTransactions {
				cancel()
			} else {
				release <- struct{}{}
				release <- struct{}{}
			}
			err = active.Wait()
			if cancelTransactions {
				if !errors.Is(err, context.Canceled) {
					return fmt.Errorf("classifier cancellation was not propagated: %v", err)
				}
				return nil
			}
			return err
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
