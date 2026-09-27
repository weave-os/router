// Residue coverage needs the real adapters, so the database test is gated:
//
//	ROUTER_TEST_DATABASE_URL="postgres://router:router@127.0.0.1:5432/router?sslmode=disable&search_path=router" \
//	  go test ./scripts/serving_admission_check -count=1
package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
