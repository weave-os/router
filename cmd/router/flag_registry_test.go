package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
)

type publishFunc func(context.Context, []flags.PublishedDefinition) error

func (f publishFunc) Publish(ctx context.Context, defs []flags.PublishedDefinition) error {
	return f(ctx, defs)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPublishFlagRegistryWithRetryRecoversFromColdStartFailures(t *testing.T) {
	attempts := 0
	want := []flags.PublishedDefinition{{Definition: flags.Registry[0], DeploymentDefault: "false"}}
	var got []flags.PublishedDefinition
	publisher := publishFunc(func(_ context.Context, defs []flags.PublishedDefinition) error {
		attempts++
		if attempts < 3 {
			return errors.New("refresh failed: context deadline exceeded")
		}
		got = defs
		return nil
	})

	publishFlagRegistryWithRetry(context.Background(), discardLogger(), publisher, want, time.Second, time.Millisecond)

	assert.Equal(t, 3, attempts)
	assert.Equal(t, want, got)
}

func TestPublishFlagRegistryWithRetryBoundsEachAttempt(t *testing.T) {
	var deadlineIn time.Duration
	publisher := publishFunc(func(ctx context.Context, _ []flags.PublishedDefinition) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "each attempt must carry its own deadline")
		deadlineIn = time.Until(deadline)
		return nil
	})

	publishFlagRegistryWithRetry(context.Background(), discardLogger(), publisher, nil, 3*time.Second, time.Millisecond)

	assert.Greater(t, deadlineIn, 2*time.Second)
	assert.LessOrEqual(t, deadlineIn, 3*time.Second)
}

func TestPublishFlagRegistryWithRetryGivesUpWhenBudgetExpires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	publisher := publishFunc(func(context.Context, []flags.PublishedDefinition) error {
		attempts++
		cancel()
		return errors.New("database unreachable")
	})

	publishFlagRegistryWithRetry(ctx, discardLogger(), publisher, nil, time.Second, time.Hour)

	cancel()
	assert.Equal(t, 1, attempts)
}
