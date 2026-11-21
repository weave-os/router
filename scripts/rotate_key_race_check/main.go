package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/config"
	"weave-os/router/internal/postgres"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const concurrentRotations = 5

func main() {
	config.ConfigureLogging()
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		fmt.Println("ROUTER_TEST_DATABASE_URL not set; skipping live-DB rotate-key race check (see file header for usage)")
		return
	}
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		fail("parse database url", err)
	}
	cfg.MaxConns = int32(concurrentRotations + 2)
	cfg.MinConns = int32(concurrentRotations)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fail("connect to database", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fail("ping database", err)
	}

	if err := checkRotateKeyRace(ctx, pool); err != nil {
		fail("rotate-key race check", err)
	}
}

func fail(step string, err error) {
	fmt.Fprintf(os.Stderr, "FAIL: %s: %v\n", step, err)
	os.Exit(1)
}

func checkRotateKeyRace(ctx context.Context, pool *pgxpool.Pool) error {
	repo := postgres.NewRepository(pool, auth.NoOpEncryptor{})

	listHold := newListHold(concurrentRotations)
	apiKeys := &holdingAPIKeyRepo{inner: repo.APIKeys, hold: listHold}

	svc := auth.NewService(
		repo.Installations,
		apiKeys,
		repo.ExternalAPIKeys,
		repo.Users,
		auth.NoOpAPIKeyCache{},
		nil,
		time.Now,
	)

	install, err := repo.Installations.Create(ctx, auth.CreateInstallationParams{
		ExternalID: "org_rotate_race_" + uuid.NewString()[:8],
		Name:       "RotateAPIKey race repro",
	})
	if err != nil {
		return fmt.Errorf("create installation: %w", err)
	}
	fmt.Printf("installation_id=%s\n", install.ID)

	name := "race-repro-key"
	issued, _, err := svc.IssueAPIKey(ctx, install.ID, &name, nil)
	if err != nil {
		return fmt.Errorf("issue initial key: %w", err)
	}
	fmt.Printf("initial_key_id=%s\n", issued.ID)

	var (
		ready   sync.WaitGroup
		done    sync.WaitGroup
		start   = make(chan struct{})
		okCount atomic.Int64
		stopMon = make(chan struct{})
		peak    atomic.Int32
	)
	ready.Add(concurrentRotations)
	done.Add(concurrentRotations)

	go func() {
		for {
			select {
			case <-stopMon:
				return
			default:
				if c := int32(pool.Stat().AcquiredConns()); c > peak.Load() {
					peak.Store(c)
				}
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	for i := 0; i < concurrentRotations; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_, _, err := svc.RotateAPIKey(ctx, install.ID, issued.ID, nil)
			if err == nil {
				okCount.Add(1)
			} else {
				fmt.Printf("RotateAPIKey error: %v\n", err)
			}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(stopMon)

	keys, err := repo.APIKeys.ListForInstallation(ctx, install.ID)
	if err != nil {
		return fmt.Errorf("list API keys: %w", err)
	}

	fmt.Printf("concurrent_calls=%d\n", concurrentRotations)
	fmt.Printf("rotate_successes=%d\n", okCount.Load())
	fmt.Printf("active_keys=%d\n", len(keys))
	fmt.Printf("pool_max_conns=%d\n", pool.Stat().MaxConns())
	fmt.Printf("pool_peak_acquired_conns=%d\n", peak.Load())
	fmt.Printf("list_hold_releases=%d\n", listHold.releases.Load())
	for _, k := range keys {
		fmt.Printf("  active key_id=%s prefix=%s\n", k.ID, k.KeyPrefix)
	}
	if okCount.Load() > 1 && len(keys) > 1 {
		fmt.Println("RACE CONFIRMED: multiple RotateAPIKey calls succeeded and multiple active keys remain")
	} else {
		fmt.Println("race not observed this run (only one successor / one success)")
	}
	return nil
}

type listHold struct {
	n        int
	mu       sync.Mutex
	arrived  int
	release  chan struct{}
	releases atomic.Int64
}

func newListHold(n int) *listHold {
	return &listHold{n: n, release: make(chan struct{})}
}

func (h *listHold) Wait() {
	h.mu.Lock()
	h.arrived++
	release := h.release
	if h.arrived == h.n {
		h.releases.Add(1)
		close(h.release)
		h.arrived = 0
		h.release = make(chan struct{})
	}
	h.mu.Unlock()
	<-release
}

type holdingAPIKeyRepo struct {
	inner auth.APIKeyRepository
	hold  *listHold
}

func (r *holdingAPIKeyRepo) Create(ctx context.Context, params auth.CreateAPIKeyParams) (*auth.APIKey, error) {
	return r.inner.Create(ctx, params)
}

func (r *holdingAPIKeyRepo) GetActiveByHashWithInstallation(ctx context.Context, keyHash string) (*auth.APIKey, *auth.Installation, error) {
	return r.inner.GetActiveByHashWithInstallation(ctx, keyHash)
}

func (r *holdingAPIKeyRepo) ListForInstallation(ctx context.Context, installationID string) ([]*auth.APIKey, error) {
	keys, err := r.inner.ListForInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	r.hold.Wait()
	return keys, nil
}

func (r *holdingAPIKeyRepo) MarkUsed(ctx context.Context, id string) (bool, error) {
	return r.inner.MarkUsed(ctx, id)
}

func (r *holdingAPIKeyRepo) SoftDelete(ctx context.Context, installationID, id string) (int64, error) {
	return r.inner.SoftDelete(ctx, installationID, id)
}
