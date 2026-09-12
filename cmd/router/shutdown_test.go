package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
)

type heldExporter struct{ started chan struct{} }

func (e heldExporter) Shutdown(ctx context.Context) error {
	close(e.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestDrainsShareOneDeadline(t *testing.T) {
	workers := observability.NewObservationWorkers()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	started := make(chan struct{})
	require.True(t, workers.Database.Submit(observability.WorkAttempt, nil, time.Minute, log, func(ctx context.Context, _ []byte) error { close(started); <-ctx.Done(); return ctx.Err() }))
	<-started
	exporter := heldExporter{started: make(chan struct{})}
	apmStarted := make(chan struct{})
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go func() {
		drainObservations(ctx, workers, exporter, func(ctx context.Context) { close(apmStarted); <-ctx.Done() }, log)
		close(done)
	}()
	select {
	case <-exporter.started:
	case <-time.After(time.Second):
		t.Fatal("exporter was serialized behind DB drain")
	}
	select {
	case <-apmStarted:
	case <-time.After(time.Second):
		t.Fatal("APM was serialized behind exporter drain")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drains exceeded shared deadline")
	}
	assert.NoError(t, workers.Shutdown(context.Background()))
}
