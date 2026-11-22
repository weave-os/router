package routingpolicy

import (
	"context"
	"sync"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/candidates"
)

const GlobalAutomaticExclusionTTL = time.Minute

type GlobalAutomaticExclusionCache struct {
	store candidates.GlobalAutomaticExclusionStore

	mu          sync.Mutex
	byModel     map[string]string
	refreshedAt time.Time
	loaded      bool
	refreshing  bool
}

func NewGlobalAutomaticExclusionCache(store candidates.GlobalAutomaticExclusionStore) *GlobalAutomaticExclusionCache {
	return &GlobalAutomaticExclusionCache{store: store}
}

func (c *GlobalAutomaticExclusionCache) Snapshot(ctx context.Context, now func() time.Time) map[string]string {
	c.mu.Lock()
	if c.loaded && now().Sub(c.refreshedAt) < GlobalAutomaticExclusionTTL {
		byModel := c.byModel
		c.mu.Unlock()
		return byModel
	}
	if c.refreshing {
		byModel := c.byModel
		c.mu.Unlock()
		return byModel
	}
	c.refreshing = true
	stale := c.byModel
	c.mu.Unlock()

	byModel, err := c.store.ListGlobalAutomaticRoutingExclusions(ctx)
	c.mu.Lock()
	c.refreshing = false
	if err != nil {
		c.mu.Unlock()
		observability.FromContext(ctx).Error("Failed to refresh global automatic-routing exclusions",
			"err", err,
			"serving_stale_snapshot", c.loaded,
			"cached_model_count", len(stale),
		)
		return stale
	}
	c.byModel = byModel
	c.refreshedAt = now()
	c.loaded = true
	c.mu.Unlock()
	return byModel
}
