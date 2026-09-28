// Command database_budget_check verifies database wait budgets on a migrated disposable database.
// Run with ROUTER_TEST_DATABASE_URL set to a local migrated fixture.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/postgres/poolconfig"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

const maximumObservedWait = 3 * time.Second

func main() {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		slog.Error("ROUTER_TEST_DATABASE_URL must name a local disposable migrated database")
		os.Exit(1)
	}
	if err := check(dsn); err != nil {
		slog.Error("Database budget check failed", "err", err)
		os.Exit(1)
	}
	slog.Info("Database budget check passed: lock wait, pool acquisition, connection recovery")
}

func check(dsn string) (checkErr error) {
	parsedDSN, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("parse test database URL: %w", err)
	}
	host := strings.ToLower(parsedDSN.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != "postgres" {
		return fmt.Errorf("refuse non-local test database host %q", host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return err
	}
	poolConfig.MaxConns = 2
	poolconfig.ConfigureTimeouts(poolConfig)
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	var configuredLockTimeout, configuredStatementTimeout string
	if err := pool.QueryRow(ctx, "SHOW lock_timeout").Scan(&configuredLockTimeout); err != nil {
		return fmt.Errorf("read configured lock timeout: %w", err)
	}
	if err := pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&configuredStatementTimeout); err != nil {
		return fmt.Errorf("read configured statement timeout: %w", err)
	}
	if configuredLockTimeout != "250ms" || configuredStatementTimeout != "5s" {
		return fmt.Errorf("database timeouts = lock %q, statement %q; want 250ms and 5s", configuredLockTimeout, configuredStatementTimeout)
	}
	repository := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	installation, err := repository.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "Database budget check"})
	if err != nil {
		return fmt.Errorf("create installation fixture: %w", err)
	}
	var lockConnection *pgxpool.Conn
	defer func() {
		if lockConnection != nil {
			rollbackCtx, cancelRollback := context.WithTimeout(context.Background(), time.Second)
			_, rollbackErr := lockConnection.Exec(rollbackCtx, "ROLLBACK")
			cancelRollback()
			lockConnection.Release()
			checkErr = errors.Join(checkErr, rollbackErr)
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
		_, cleanupErr := pool.Exec(cleanupCtx, "DELETE FROM router.model_router_installations WHERE id = $1", installation.ID)
		cancelCleanup()
		checkErr = errors.Join(checkErr, cleanupErr)
	}()
	lockConnection, err = pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire lock connection: %w", err)
	}
	if _, err := lockConnection.Exec(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("begin lock transaction: %w", err)
	}
	if _, err := lockConnection.Exec(ctx, "SELECT id FROM router.model_router_installations WHERE id = $1 FOR UPDATE", installation.ID); err != nil {
		return fmt.Errorf("lock installation fixture: %w", err)
	}
	pinStore := postgres.NewSessionPinRepo(pool)
	pin := sessionpin.Pin{
		SessionKey:     sessionKey(),
		Role:           sessionpin.DefaultRole,
		InstallationID: uuid.MustParse(installation.ID),
		Provider:       string(providers.ProviderAnthropic),
		Model:          string(catalog.ModelIDClaudeSonnet46),
		PinnedUntil:    time.Now().Add(time.Hour),
	}
	started := time.Now()
	err = pinStore.Upsert(context.Background(), pin)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		return fmt.Errorf("locked pin insert error = %v, want PostgreSQL lock timeout (55P03)", err)
	}
	if err := verifyBoundedWait("locked pin insert", time.Since(started), 100*time.Millisecond); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, "SELECT 1"); err != nil {
		return fmt.Errorf("pool connection was not recovered after lock timeout: %w", err)
	}

	started = time.Now()
	_, err = dbbudget.NewDBTX(pool).Exec(context.Background(), "SELECT pg_sleep(10)")
	if !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("slow statement error = %v, want context deadline exceeded", err)
	}
	if err := verifyBoundedWait("slow statement", time.Since(started), time.Second); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, "SELECT 1"); err != nil {
		return fmt.Errorf("pool connection was not recovered after statement deadline: %w", err)
	}

	occupiedConnection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("occupy remaining pool connection: %w", err)
	}
	started = time.Now()
	err = pinStore.Upsert(context.Background(), pin)
	occupiedConnection.Release()
	if !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("pool-exhausted pin insert error = %v, want context deadline exceeded", err)
	}
	if err := verifyBoundedWait("pool-exhausted pin insert", time.Since(started), time.Second); err != nil {
		return err
	}

	rollbackCtx, cancelRollback := context.WithTimeout(context.Background(), time.Second)
	_, err = lockConnection.Exec(rollbackCtx, "ROLLBACK")
	cancelRollback()
	if err != nil {
		return fmt.Errorf("release installation lock: %w", err)
	}
	lockConnection.Release()
	lockConnection = nil
	if err := pinStore.Upsert(ctx, pin); err != nil {
		return fmt.Errorf("pin write did not recover after releasing lock: %w", err)
	}
	_, found, err := pinStore.Get(ctx, pin.SessionKey, pin.Role)
	if err != nil {
		return fmt.Errorf("read pin after recovery: %w", err)
	}
	if !found {
		return errors.New("pin write was not readable after recovery")
	}
	return nil
}

func verifyBoundedWait(operation string, elapsed, minimum time.Duration) error {
	if elapsed < minimum || elapsed > maximumObservedWait {
		return fmt.Errorf("%s took %s, want between %s and %s", operation, elapsed, minimum, maximumObservedWait)
	}
	return nil
}

func sessionKey() [sessionpin.SessionKeyLen]byte {
	key := [sessionpin.SessionKeyLen]byte{}
	value := uuid.New()
	copy(key[:], value[:])
	return key
}
