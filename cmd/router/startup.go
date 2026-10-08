package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v5"
	"weave-os/router/internal/health"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

const startupTimeout = 170 * time.Second

type startupState struct {
	completionMu sync.Mutex
	ready        atomic.Bool
	taskErrors   chan error
}

func (s *startupState) reportTaskFailure(err error) {
	s.completionMu.Lock()
	defer s.completionMu.Unlock()
	s.taskErrors <- err
}

func (s *startupState) complete(ctx context.Context, checker readinessChecker, capacity *health.Capacity) error {
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err := checker.CheckHealth(checkCtx)
	if err != nil {
		return fmt.Errorf("final startup readiness check: %w", err)
	}
	if err := checkCtx.Err(); err != nil {
		return err
	}
	capacity.SampleResources()
	if !capacity.Snapshot().Ready {
		return fmt.Errorf("worker has no capacity after initialization")
	}
	// Failure publication and the readiness latch must commit in one order.
	s.completionMu.Lock()
	defer s.completionMu.Unlock()
	select {
	case err := <-s.taskErrors:
		return err
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.ready.Store(true)
	return nil
}

func warmStartupDatabase(ctx context.Context, log *slog.Logger, warm func(context.Context) error) error {
	started := time.Now()
	_, err := backoff.Retry(ctx, func() (struct{}, error) { return struct{}{}, warm(ctx) },
		backoff.WithBackOff(backoff.NewConstantBackOff(time.Second)), backoff.WithMaxElapsedTime(0),
		backoff.WithNotify(func(err error, delay time.Duration) {
			log.Warn("Startup database is not ready; retrying", "retry_after", delay, "err", err)
		}))
	if err != nil {
		return fmt.Errorf("initialize serving database: %w", err)
	}
	log.Info("Startup database initialized", "elapsed", time.Since(started))
	return nil
}

func warmLocalRouter(ctx context.Context, scorer router.Router) error {
	decision, err := scorer.Route(ctx, router.Request{PromptText: "Explain how to reverse a short list of integers.", EstimatedInputTokens: 12})
	if err != nil {
		return fmt.Errorf("exercise local scoring: %w", err)
	}
	if decision.Model == "" || decision.Provider == "" {
		return fmt.Errorf("local scoring returned an empty decision")
	}
	if decision.Metadata != nil && (math.IsNaN(float64(decision.Metadata.ChosenScore)) || math.IsInf(float64(decision.Metadata.ChosenScore), 0)) {
		return fmt.Errorf("local scoring returned a nonfinite score")
	}
	return nil
}

func warmClassifier(ctx context.Context, previewer policy.RoutePreviewer) error {
	preview, err := previewer.PreviewRoute(ctx, router.Request{PromptText: "Explain how to reverse a short list of integers.", EstimatedInputTokens: 12})
	if err != nil {
		return fmt.Errorf("exercise startup classifier: %w", err)
	}
	if len(preview.ClassOrder) == 0 || len(preview.ClassProbabilities) != len(preview.ClassOrder) {
		return fmt.Errorf("startup classifier returned an invalid probability shape")
	}
	for _, label := range preview.ClassOrder {
		probability, ok := preview.ClassProbabilities[label]
		if !ok || math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
			return fmt.Errorf("startup classifier returned an invalid probability")
		}
	}
	return nil
}

func runEssentialTask(ctx context.Context, run func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("essential serving task panicked: %v", recovered)
		}
	}()
	run()
	if ctx.Err() == nil {
		return fmt.Errorf("essential serving task stopped unexpectedly")
	}
	return nil
}
