package routingpolicy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/routingpolicy"
)

// stubGlobalExclusionStore serves a fixed list and counts reads so the cache's
// TTL behavior is observable.
type stubGlobalExclusionStore struct {
	byModel map[string]string
	err     error
	calls   int
}

func (s *stubGlobalExclusionStore) ListGlobalAutomaticRoutingExclusions(context.Context) (map[string]string, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.byModel, nil
}

func TestGlobalAutomaticExclusions_ServesCachedSnapshotWithinTTL(t *testing.T) {
	store := &stubGlobalExclusionStore{byModel: map[string]string{"claude-opus-5": "too expensive"}}
	cache := routingpolicy.NewGlobalAutomaticExclusionCache(store)

	first := cache.Snapshot(context.Background(), time.Now)
	second := cache.Snapshot(context.Background(), time.Now)

	assert.Equal(t, map[string]string{"claude-opus-5": "too expensive"}, first)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, store.calls, "a second read inside the TTL must not re-query")
}

// A control-plane outage must not disable routing, so a cold cache that cannot
// be loaded reports nothing disabled and retries on the next turn.
func TestGlobalAutomaticExclusions_ColdReadFailureFailsOpen(t *testing.T) {
	store := &stubGlobalExclusionStore{err: errors.New("router database unreachable")}
	cache := routingpolicy.NewGlobalAutomaticExclusionCache(store)

	assert.Empty(t, cache.Snapshot(context.Background(), time.Now))
	assert.Empty(t, cache.Snapshot(context.Background(), time.Now))
	assert.Equal(t, 2, store.calls, "a failed cold read must retry rather than cache emptiness for a full TTL")
}

func TestGlobalAutomaticExclusions_RefreshFailureKeepsLastSnapshot(t *testing.T) {
	store := &stubGlobalExclusionStore{byModel: map[string]string{"claude-opus-5": ""}}
	cache := routingpolicy.NewGlobalAutomaticExclusionCache(store)
	now := time.Now()
	clock := func() time.Time { return now }

	require.NotEmpty(t, cache.Snapshot(context.Background(), clock))
	store.err = errors.New("router database unreachable")
	now = now.Add(2 * routingpolicy.GlobalAutomaticExclusionTTL)

	assert.Equal(t, map[string]string{"claude-opus-5": ""}, cache.Snapshot(context.Background(), clock))
	assert.Equal(t, 2, store.calls)
}
