package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestSafeGoRecoversFromPanic proves a panic inside the wrapped fn is
// recovered and logged rather than propagating out of the goroutine (which
// would crash the process).
func TestSafeGoRecoversFromPanic(t *testing.T) {
	var buf concurrentLogBuffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	assert.NotPanics(t, func() {
		SafeGo(log, time.Second, "test-goroutine", func(ctx context.Context) {
			panic("boom")
		})
		waitForLog(t, &buf, "Background goroutine panicked")
	})

	assert.Contains(t, buf.String(), "Background goroutine panicked")
	assert.Contains(t, buf.String(), "test-goroutine")
	assert.Contains(t, buf.String(), "boom")
}

// TestSafeGoRunsFnToCompletion proves a non-panicking fn runs normally with
// a bounded context derived from context.Background(), independent of any
// caller-supplied ctx.
func TestSafeGoRunsFnToCompletion(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	done := make(chan struct{})

	SafeGo(log, time.Second, "test-goroutine", func(ctx context.Context) {
		defer close(done)
		assert.NoError(t, ctx.Err())
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fn did not run within timeout")
	}
	assert.Empty(t, buf.String())
}

func TestSafeGoContextRetainsSnapshotValuesAfterCancellation(t *testing.T) {
	type snapshotKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), snapshotKey{}, "admitted-release"))
	cancel()
	completed := make(chan context.Context, 1)
	SafeGoContext(parent, slog.Default(), time.Second, "admitted-outcome", func(ctx context.Context) {
		assert.NoError(t, ctx.Err())
		assert.Equal(t, "admitted-release", ctx.Value(snapshotKey{}))
		_, bounded := ctx.Deadline()
		assert.True(t, bounded)
		completed <- ctx
	})
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("outcome did not complete")
	}
}

// waitForLog polls buf until it contains substr or fails the test after a
// bounded wait, since the goroutine under test runs concurrently.
func waitForLog(t *testing.T, buf *concurrentLogBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected log containing %q, got: %s", substr, buf.String())
}

type concurrentLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *concurrentLogBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(payload)
}

func (b *concurrentLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
