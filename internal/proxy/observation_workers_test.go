package proxy_test

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/observability"

	"github.com/stretchr/testify/require"
)

func testObservationWorkers(t *testing.T) *observability.ObservationWorkers {
	t.Helper()
	workers := observability.NewObservationWorkers()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = workers.Shutdown(ctx)
	})
	return workers
}

// drainObservationWorkers waits for both lanes to finish admitted jobs so an
// absence assertion on an async reporter cannot pass while a job is queued.
func drainObservationWorkers(t *testing.T, workers *observability.ObservationWorkers) {
	t.Helper()
	deadline, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, workers.Shutdown(deadline), "observation workers did not drain within the deadline")
}
