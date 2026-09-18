// Command session_pin_output_limit_check exercises the session-pin
// output-limit marker lifecycle (UpsertSessionPin preserve/reset rules and the
// atomic UpdateSessionPinUsage write) against a migrated disposable database.
// An in-memory fake cannot prove the SQL, so this runs the real adapter inside
// one transaction that is always rolled back. Set ROUTER_TEST_DATABASE_URL to
// opt in; unset means the check is skipped, not passed.
//
//	ROUTER_TEST_DATABASE_URL="postgres://router:router@localhost:5432/router?sslmode=disable&search_path=router" \
//	  go run ./scripts/session_pin_output_limit_check
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Synthetic serving identities; the SQL under test treats them as opaque.
const (
	modelPrimary   = "output-limit-check-model-a"
	modelSecondary = "output-limit-check-model-b"
)

// hmmHistoryRole mirrors the proxy's unexported HMM-history role suffix so the
// check covers the second role the guard reads. Only (session_key, role)
// isolation matters here, not the proxy's role semantics.
const hmmHistoryRole = sessionpin.DefaultRole + "_hmm_history"

func main() {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		slog.Info("ROUTER_TEST_DATABASE_URL unset; skipping session-pin output-limit database check")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := checkOutputLimitLifecycle(ctx, dsn); err != nil {
		slog.Error("Session-pin output-limit database check failed", "err", err)
		os.Exit(1)
	}
	slog.Info("Session-pin output-limit database checks passed: default null, healthy high-output unmarked, confirmed cap round trip, same-strategy refresh and model change preserve, healthy clear, stale-strategy no-op, strategy reset, role isolation, Consume returns marker, rollback")
}

func checkOutputLimitLifecycle(ctx context.Context, dsn string) (checkErr error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		checkErr = errors.Join(checkErr, conn.Close(closeCtx))
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	// Every fixture lives inside this transaction; rolling it back is the only
	// cleanup, on success and failure alike.
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		checkErr = errors.Join(checkErr, tx.Rollback(rollbackCtx))
	}()

	repositories := postgres.NewRepository(tx, auth.NoOpEncryptor{})
	installation, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "Session-pin output-limit check"})
	if err != nil {
		return err
	}
	installationID, err := uuid.Parse(installation.ID)
	if err != nil {
		return err
	}
	store := postgres.NewSessionPinRepo(tx)
	reader := postgres.NewSessionPinRepo(tx)

	var key [sessionpin.SessionKeyLen]byte
	digest := sha256.Sum256([]byte(uuid.NewString()))
	copy(key[:], digest[:])

	// Postgres stores microseconds; truncate so Equal survives the round trip.
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	turnEnd := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

	newPin := func(role, model string, strategy router.Strategy) sessionpin.Pin {
		return sessionpin.Pin{
			SessionKey:     key,
			Role:           role,
			InstallationID: installationID,
			Provider:       providers.ProviderAnthropic,
			Model:          model,
			Reason:         "output-limit lifecycle check",
			Strategy:       strategy,
			TurnCount:      1,
			PinnedUntil:    time.Now().Add(time.Hour),
		}
	}
	usage := func(strategy router.Strategy, model string, endedAt time.Time, capped bool) sessionpin.Usage {
		return sessionpin.Usage{
			Strategy:           strategy,
			InputTokens:        150000,
			OutputTokens:       32000,
			EndedAt:            endedAt,
			OutputLimitReached: capped,
			ServedModel:        model,
			ServedProvider:     providers.ProviderAnthropic,
		}
	}
	get := func(role string) (sessionpin.Pin, error) {
		pin, found, err := reader.Get(ctx, key, role)
		if err != nil {
			return sessionpin.Pin{}, err
		}
		if !found {
			return sessionpin.Pin{}, fmt.Errorf("pin row for role %q missing", role)
		}
		return pin, nil
	}

	// Fresh rows for both roles start with no evidence.
	for _, role := range []string{sessionpin.DefaultRole, hmmHistoryRole} {
		if err := store.Upsert(ctx, newPin(role, modelPrimary, router.StrategyCluster)); err != nil {
			return err
		}
		pin, err := get(role)
		if err != nil {
			return err
		}
		if !pin.LastOutputLimitAt.IsZero() || !pin.LastTurnEndedAt.IsZero() {
			return fmt.Errorf("fresh %q row carries evidence: marker=%v ended=%v", role, pin.LastOutputLimitAt, pin.LastTurnEndedAt)
		}
	}

	// A healthy high-output turn records usage without a marker.
	if err := store.UpdateUsage(ctx, key, sessionpin.DefaultRole, usage(router.StrategyCluster, modelPrimary, turnEnd(1), false)); err != nil {
		return err
	}
	pin, err := get(sessionpin.DefaultRole)
	if err != nil {
		return err
	}
	if !pin.LastOutputLimitAt.IsZero() || !pin.LastTurnEndedAt.Equal(turnEnd(1)) || pin.LastOutputTokens != 32000 || pin.LastServedModel != modelPrimary {
		return fmt.Errorf("healthy high-output write: marker=%v ended=%v out=%d served=%q", pin.LastOutputLimitAt, pin.LastTurnEndedAt, pin.LastOutputTokens, pin.LastServedModel)
	}

	// A confirmed cap binds the marker to the same turn-end instant.
	if err := store.UpdateUsage(ctx, key, sessionpin.DefaultRole, usage(router.StrategyCluster, modelPrimary, turnEnd(2), true)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if !pin.LastOutputLimitAt.Equal(turnEnd(2)) || !pin.LastOutputLimitAt.Equal(pin.LastTurnEndedAt) {
		return fmt.Errorf("confirmed cap: marker=%v ended=%v", pin.LastOutputLimitAt, pin.LastTurnEndedAt)
	}

	// An older healthy completion must not erase newer cap evidence or regress
	// the usage and serving identity that the next continuation reads.
	if err := store.UpdateUsage(ctx, key, sessionpin.DefaultRole, usage(router.StrategyCluster, modelSecondary, turnEnd(1), false)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if !pin.LastOutputLimitAt.Equal(turnEnd(2)) || !pin.LastTurnEndedAt.Equal(turnEnd(2)) || pin.LastOutputTokens != 32000 || pin.LastServedModel != modelPrimary {
		return fmt.Errorf("older healthy write regressed newest cap: marker=%v ended=%v out=%d served=%q", pin.LastOutputLimitAt, pin.LastTurnEndedAt, pin.LastOutputTokens, pin.LastServedModel)
	}

	// Same-strategy refresh preserves the marker with the other usage fields.
	if err := store.Upsert(ctx, newPin(sessionpin.DefaultRole, modelPrimary, router.StrategyCluster)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if !pin.LastOutputLimitAt.Equal(turnEnd(2)) || !pin.LastTurnEndedAt.Equal(turnEnd(2)) || pin.LastServedModel != modelPrimary || pin.TurnCount != 2 {
		return fmt.Errorf("same-strategy refresh: marker=%v ended=%v served=%q turns=%d", pin.LastOutputLimitAt, pin.LastTurnEndedAt, pin.LastServedModel, pin.TurnCount)
	}

	// Re-anchoring to another model under the same strategy keeps the prior
	// served outcome: the guard names LastServedModel, not the new anchor.
	if err := store.Upsert(ctx, newPin(sessionpin.DefaultRole, modelSecondary, router.StrategyCluster)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if pin.Model != modelSecondary || pin.LastServedModel != modelPrimary || !pin.LastOutputLimitAt.Equal(turnEnd(2)) || !pin.LastOutputLimitAt.Equal(pin.LastTurnEndedAt) {
		return fmt.Errorf("model change same strategy: model=%q served=%q marker=%v ended=%v", pin.Model, pin.LastServedModel, pin.LastOutputLimitAt, pin.LastTurnEndedAt)
	}

	// A later healthy turn clears the marker in the same write as its usage.
	if err := store.UpdateUsage(ctx, key, sessionpin.DefaultRole, usage(router.StrategyCluster, modelSecondary, turnEnd(3), false)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if !pin.LastOutputLimitAt.IsZero() || !pin.LastTurnEndedAt.Equal(turnEnd(3)) || pin.LastServedModel != modelSecondary {
		return fmt.Errorf("healthy clear: marker=%v ended=%v served=%q", pin.LastOutputLimitAt, pin.LastTurnEndedAt, pin.LastServedModel)
	}

	// A cap reported by a strategy that no longer owns the row must not mark it.
	if err := store.UpdateUsage(ctx, key, sessionpin.DefaultRole, usage(router.StrategyHMMBeta, modelSecondary, turnEnd(4), true)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if !pin.LastOutputLimitAt.IsZero() || !pin.LastTurnEndedAt.Equal(turnEnd(3)) {
		return fmt.Errorf("stale-strategy cap write mutated row: marker=%v ended=%v", pin.LastOutputLimitAt, pin.LastTurnEndedAt)
	}

	// ...and a stale healthy write must not clear a genuine marker either.
	if err := store.UpdateUsage(ctx, key, sessionpin.DefaultRole, usage(router.StrategyCluster, modelSecondary, turnEnd(5), true)); err != nil {
		return err
	}
	if err := store.UpdateUsage(ctx, key, sessionpin.DefaultRole, usage(router.StrategyHMMBeta, modelSecondary, turnEnd(6), false)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if !pin.LastOutputLimitAt.Equal(turnEnd(5)) || !pin.LastTurnEndedAt.Equal(turnEnd(5)) {
		return fmt.Errorf("stale-strategy healthy write mutated row: marker=%v ended=%v", pin.LastOutputLimitAt, pin.LastTurnEndedAt)
	}

	// The history role was never written and must not have inherited anything.
	history, err := get(hmmHistoryRole)
	if err != nil {
		return err
	}
	if !history.LastOutputLimitAt.IsZero() || !history.LastTurnEndedAt.IsZero() {
		return fmt.Errorf("history role inherited default-role evidence: marker=%v ended=%v", history.LastOutputLimitAt, history.LastTurnEndedAt)
	}

	// A strategy replacement resets the marker with the other prior usage.
	if err := store.Upsert(ctx, newPin(sessionpin.DefaultRole, modelPrimary, router.StrategyHMMBeta)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if pin.Strategy != router.StrategyHMMBeta || !pin.LastOutputLimitAt.IsZero() || !pin.LastTurnEndedAt.IsZero() || pin.LastServedModel != "" || pin.LastOutputTokens != 0 {
		return fmt.Errorf("strategy reset: strategy=%q marker=%v ended=%v served=%q out=%d", pin.Strategy, pin.LastOutputLimitAt, pin.LastTurnEndedAt, pin.LastServedModel, pin.LastOutputTokens)
	}

	// Marking the history role leaves the default role untouched, and Consume
	// hands the marker back with the row it removes.
	if err := store.UpdateUsage(ctx, key, hmmHistoryRole, usage(router.StrategyCluster, modelPrimary, turnEnd(7), true)); err != nil {
		return err
	}
	if pin, err = get(sessionpin.DefaultRole); err != nil {
		return err
	}
	if !pin.LastOutputLimitAt.IsZero() {
		return fmt.Errorf("history-role cap leaked into default role: marker=%v", pin.LastOutputLimitAt)
	}
	consumed, found, err := reader.Consume(ctx, key, hmmHistoryRole, router.StrategyCluster)
	if err != nil {
		return err
	}
	if !found || !consumed.LastOutputLimitAt.Equal(turnEnd(7)) || !consumed.LastOutputLimitAt.Equal(consumed.LastTurnEndedAt) || consumed.LastServedModel != modelPrimary {
		return fmt.Errorf("consume: found=%t marker=%v ended=%v served=%q", found, consumed.LastOutputLimitAt, consumed.LastTurnEndedAt, consumed.LastServedModel)
	}
	if _, found, err = reader.Get(ctx, key, hmmHistoryRole); err != nil {
		return err
	}
	if found {
		return errors.New("consumed history row still present")
	}
	return nil
}
