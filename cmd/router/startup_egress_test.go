package main

import (
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"
	"weave-os/router/internal/providers"
)

func TestStartupEgressRejectsInvalidOrigins(t *testing.T) {
	for _, origin := range []string{"http://example.com", "https://", "https://user:secret@example.com", "https://example.com/v1", "https://example.com?token=secret", "https://example.com?", "https://example.com#fragment", "https://example.com,", "https://example.com:bad"} {
		t.Run(origin, func(t *testing.T) {
			_, err := newStartupEgressProbe(origin)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret")
		})
	}
}

type startupProvider struct {
	providers.Client
	warm func(context.Context, string) (bool, error)
}

func (p startupProvider) WarmTransport(ctx context.Context, origin string) (bool, error) {
	return p.warm(ctx, origin)
}

func TestStartupEgressWaitsForEveryRetainedClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		probe, err := newStartupEgressProbe("")
		require.NoError(t, err)
		calls := 0
		client := startupProvider{warm: func(ctx context.Context, _ string) (bool, error) {
			calls++
			if calls == 1 {
				return true, errors.New("network unavailable")
			}
			return true, nil
		}}
		err = probe.wait(context.Background(), slog.Default(), map[string]providers.Client{providers.ProviderOpenAI: client}, map[string]struct{}{providers.ProviderOpenAI: {}}, nil)
		require.NoError(t, err)
		require.Equal(t, 2, calls)
	})
}

func TestStartupEgressHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		probe, err := newStartupEgressProbe("")
		require.NoError(t, err)
		client := startupProvider{warm: func(ctx context.Context, _ string) (bool, error) { <-ctx.Done(); return true, ctx.Err() }}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = probe.wait(ctx, slog.Default(), map[string]providers.Client{providers.ProviderOpenAI: client}, map[string]struct{}{providers.ProviderOpenAI: {}}, nil)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestStartupEgressRequiresRetainedClientOrInitializedDependency(t *testing.T) {
	probe, err := newStartupEgressProbe("https://pubsub.googleapis.com")
	require.NoError(t, err)
	require.ErrorContains(t, probe.wait(context.Background(), slog.Default(), nil, nil, nil), "no retained serving client")
	require.NoError(t, probe.wait(context.Background(), slog.Default(), nil, nil, map[string]struct{}{"https://pubsub.googleapis.com": {}}))
}

func TestStartupEgressDoesNotInitializeUnknownTenantProviders(t *testing.T) {
	probe, err := newStartupEgressProbe("")
	require.NoError(t, err)
	client := startupProvider{warm: func(context.Context, string) (bool, error) {
		t.Fatal("tenant provider must remain lazy")
		return false, nil
	}}
	require.NoError(t, probe.wait(context.Background(), slog.Default(), map[string]providers.Client{providers.ProviderOpenAIGateway: client}, nil, nil))
}
