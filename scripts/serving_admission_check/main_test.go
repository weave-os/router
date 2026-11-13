// Residue coverage needs the real adapters, so the database test is gated:
//
//	ROUTER_TEST_DATABASE_URL="postgres://router:router@127.0.0.1:5432/router?sslmode=disable&search_path=router" \
//	  go test ./scripts/serving_admission_check -count=1
package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres/serving"
)

func TestFixtureDSNRequiresLoopback(t *testing.T) {
	loopback := []string{
		"postgres://router:router@127.0.0.1:5432/router?sslmode=disable&search_path=router",
		"postgres://router:router@localhost:5432/router",
		"postgres://router:router@[::1]:5432/router",
	}
	for _, dsn := range loopback {
		accepted, err := fixtureDSN(dsn)
		if err != nil {
			t.Fatalf("loopback DSN %q rejected: %v", dsn, err)
		}
		if accepted != dsn {
			t.Fatalf("loopback DSN rewritten: got %q want %q", accepted, dsn)
		}
	}
	for _, dsn := range []string{"", "postgres://router:router@db.internal:5432/router", "::not a url"} {
		if _, err := fixtureDSN(dsn); err == nil {
			t.Fatalf("DSN %q accepted", dsn)
		}
	}
}

func TestAdmissionPoolAcquisitionHasDatabaseDeadline(t *testing.T) {
	dsn, err := fixtureDSN(os.Getenv(databaseURLEnv))
	if err != nil {
		t.Skipf("%s not set to a loopback fixture: %v", databaseURLEnv, err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse fixture DSN: %v", err)
	}
	config.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect fixture database: %v", err)
	}
	defer pool.Close()
	occupied, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("occupy sole pool connection: %v", err)
	}
	defer occupied.Release()

	admissions, err := serving.NewServingAdmissionRepo(pool, policyregistry.EnvironmentProd)
	if err != nil {
		t.Fatalf("create serving admission repo: %v", err)
	}
	decisionCalled := false
	started := time.Now()
	_, _, err = admissions.Admit(ctx, uuid.NewString(), uuid.NewString(), "conversation", func(context.Context, policyregistry.SerializedAdmission) (policyregistry.SessionReleaseBinding, error) {
		decisionCalled = true
		return policyregistry.SessionReleaseBinding{}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission under pool exhaustion error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed < time.Second || elapsed > 3*time.Second {
		t.Fatalf("admission under pool exhaustion took %s, want between 1s and 3s", elapsed)
	}
	if decisionCalled {
		t.Fatal("admission decision ran without acquiring a database connection")
	}
}

// A passing run must leave the fixture tables exactly as it found them.
func TestRunLeavesNoFixtureResidue(t *testing.T) {
	dsn := os.Getenv(databaseURLEnv)
	if dsn == "" {
		t.Skipf("%s not set", databaseURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect fixture database: %v", err)
	}
	defer pool.Close()
	before := countFixtureTables(ctx, t, pool)
	if err := run(); err != nil {
		t.Fatalf("serving admission check: %v", err)
	}
	after := countFixtureTables(ctx, t, pool)
	for table, count := range after {
		if count != before[table] {
			t.Errorf("%s changed: before=%d after=%d", table, before[table], count)
		}
	}
}

func countFixtureTables(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, len(fixtureTables))
	for _, probe := range fixtureTables {
		var total int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+probe.table).Scan(&total); err != nil {
			t.Fatalf("count %s: %v", probe.table, err)
		}
		counts[probe.table] = total
	}
	return counts
}
