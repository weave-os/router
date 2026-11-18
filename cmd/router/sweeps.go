package main

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router/sessionpin"
)

func runSessionPinSweep(ctx context.Context, store sessionpin.Store) {
	logger := observability.Get()
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			if err := store.SweepExpired(sweepCtx); err != nil {
				logger.Error("Session pin sweep failed", "err", err)
			}
			cancel()
		}
	}
}

func runSessionTurnClockSweep(ctx context.Context, store *postgres.SessionTurnClockRepo) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := store.SweepExpired(sweepCtx); err != nil {
				observability.FromContext(ctx).Error("Session turn clock sweep failed", "err", err)
			}
			cancel()
		}
	}
}

func runEscalationSweep(ctx context.Context, store escalationStateSweeper) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := store.SweepExpired(sweepCtx); err != nil {
				observability.FromContext(ctx).Error("Escalation state sweep failed", "err", err)
			}
			cancel()
		}
	}
}
