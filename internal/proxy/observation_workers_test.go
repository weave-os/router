package proxy_test

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/observability"
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
