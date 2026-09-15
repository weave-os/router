package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"weave-os/router/internal/observability"

	"github.com/stretchr/testify/require"
)

type feedbackShutdownExporter struct{ stop <-chan struct{} }

func (e feedbackShutdownExporter) Shutdown(ctx context.Context) error {
	select {
	case <-e.stop:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestFeedbackProcessorStopsAlongsideExporters(t *testing.T) {
	workers := observability.NewObservationWorkers()
	stopped := make(chan struct{})
	var deadlinePresent bool
	shutdownRouter(&http.Server{}, workers, feedbackShutdownExporter{stop: stopped}, func(context.Context) {}, func(ctx context.Context) {
		_, deadlinePresent = ctx.Deadline()
		close(stopped)
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.True(t, deadlinePresent, "feedback shutdown shares the process deadline")
	require.NoError(t, workers.Shutdown(context.Background()))
}
