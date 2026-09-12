package main

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"weave-os/router/internal/observability"
)

type observationExporter interface{ Shutdown(context.Context) error }

func shutdownRouter(srv *http.Server, workers *observability.ObservationWorkers, emitter observationExporter, shutdownAPM func(context.Context), log *slog.Logger) {
	// Keep one nine-second process budget, including HTTP drain and all exporters.
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	httpCtx, httpCancel := context.WithTimeout(ctx, 6*time.Second)
	err := srv.Shutdown(httpCtx)
	httpCancel()
	if err != nil {
		log.Error("Graceful shutdown failed", "err", err)
	}
	drainObservations(ctx, workers, emitter, shutdownAPM, log)
}

func drainObservations(ctx context.Context, workers *observability.ObservationWorkers, emitter observationExporter, shutdownAPM func(context.Context), log *slog.Logger) {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		err := workers.Shutdown(ctx)
		if err != nil {
			log.Error("Observation workers shutdown incomplete", "err", err)
		}
	}()
	go func() {
		defer wg.Done()
		err := emitter.Shutdown(ctx)
		if err != nil {
			log.Error("OTel emitter shutdown incomplete", "err", err)
		}
	}()
	go func() { defer wg.Done(); shutdownAPM(ctx) }()
	wg.Wait()
}
