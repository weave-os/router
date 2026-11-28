package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"weave-os/router/internal/observability/apm"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/proxy"
)

func serveUntilShutdown(processCtx context.Context, serverErr <-chan error, srv *http.Server, logger *slog.Logger, waitForKeepWarm func(context.Context), attemptSink *proxy.AsyncAttemptSink, emitter *otel.Emitter) {
	select {
	case err := <-serverErr:
		logger.Error("Server exited with error", "err", err)
		sinkCtx, sinkCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer sinkCancel()
		closeAttemptSink(sinkCtx, logger, attemptSink)
		apmCtx, apmCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer apmCancel()
		apm.ShutdownWithContext(apmCtx)
		return
	case <-processCtx.Done():
		logger.Info("Received shutdown signal; draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	drainServer(shutdownCtx, srv, logger, waitForKeepWarm)
	sinkCtx, sinkCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer sinkCancel()
	closeAttemptSink(sinkCtx, logger, attemptSink)
	emitterCtx, emitterCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer emitterCancel()
	if err := emitter.Shutdown(emitterCtx); err != nil {
		logger.Warn("OTel emitter shutdown incomplete", "err", err)
	}
	apmCtx, apmCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer apmCancel()
	apm.ShutdownWithContext(apmCtx)
}

func closeAttemptSink(ctx context.Context, logger *slog.Logger, sink *proxy.AsyncAttemptSink) {
	if err := sink.Close(ctx); err != nil {
		logger.Warn("Inference attempt telemetry drain incomplete", "err", err)
	}
}
