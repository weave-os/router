package auth_test

import (
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lists(cluster string, models ...string) []auth.UserClusterModelList {
	return []auth.UserClusterModelList{{ClusterLabel: cluster, Models: models}}
}

func TestLRUUserClusterListCache_GetSet(t *testing.T) {
	c := auth.NewLRUUserClusterListCache(10, time.Minute)

	_, ok := c.Get("user-1")
	assert.False(t, ok)

	c.Set("inst-1", "user-1", lists("balanced", "a"))

	got, ok := c.Get("user-1")
	require.True(t, ok)
	assert.Equal(t, lists("balanced", "a"), got)
}

// Caching a successful empty result is deliberate: most users configure
// nothing, so treating empty as a miss would put a DB round trip on every
// request from every unconfigured user.
func TestLRUUserClusterListCache_CachesEmptyResult(t *testing.T) {
	c := auth.NewLRUUserClusterListCache(10, time.Minute)

	c.Set("inst-1", "user-1", nil)

	_, ok := c.Get("user-1")
	assert.True(t, ok, "an empty result must be a cache hit, not a miss")
}

// The whole point of the byInstallation index: one installation-changed
// message must evict that installation's users and leave others alone.
func TestLRUUserClusterListCache_InvalidateInstallationIsScoped(t *testing.T) {
	c := auth.NewLRUUserClusterListCache(10, time.Minute)
	c.Set("inst-1", "user-1", lists("fast", "a"))
	c.Set("inst-1", "user-2", lists("fast", "b"))
	c.Set("inst-2", "user-3", lists("fast", "c"))

	c.InvalidateInstallation("inst-1")

	_, ok1 := c.Get("user-1")
	_, ok2 := c.Get("user-2")
	_, ok3 := c.Get("user-3")
	assert.False(t, ok1)
	assert.False(t, ok2)
	assert.True(t, ok3, "another installation's users must survive")
}

func TestLRUUserClusterListCache_InvalidateUnknownInstallationIsNoOp(t *testing.T) {
	c := auth.NewLRUUserClusterListCache(10, time.Minute)
	c.Set("inst-1", "user-1", lists("fast", "a"))

	c.InvalidateInstallation("")
	c.InvalidateInstallation("inst-does-not-exist")

	_, ok := c.Get("user-1")
	assert.True(t, ok)
}

// A second invalidation after re-populating must evict again — i.e. the index
// is rebuilt on Set, not consumed once.
func TestLRUUserClusterListCache_ReindexesAfterInvalidation(t *testing.T) {
	c := auth.NewLRUUserClusterListCache(10, time.Minute)
	c.Set("inst-1", "user-1", lists("fast", "a"))
	c.InvalidateInstallation("inst-1")

	c.Set("inst-1", "user-1", lists("fast", "b"))
	c.InvalidateInstallation("inst-1")

	_, ok := c.Get("user-1")
	assert.False(t, ok, "re-populated entries must be evictable again")
}

func TestLRUUserClusterListCache_TTLExpires(t *testing.T) {
	c := auth.NewLRUUserClusterListCache(10, 10*time.Millisecond)
	c.Set("inst-1", "user-1", lists("fast", "a"))

	require.Eventually(t, func() bool {
		_, ok := c.Get("user-1")
		return !ok
	}, time.Second, 5*time.Millisecond)
}

func TestNoOpUserClusterListCache_AlwaysMisses(t *testing.T) {
	var c auth.UserClusterListCache = auth.NoOpUserClusterListCache{}

	c.Set("inst-1", "user-1", lists("fast", "a"))

	_, ok := c.Get("user-1")
	assert.False(t, ok)
}
