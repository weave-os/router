package proxy

import (
	"context"
	"sync"
	"time"

	"workweave/router/internal/observability"
)

// globalAutomaticExclusionTTL bounds how stale a replica's snapshot may be.
// Every replica refreshes independently, so this is also the worst-case
// propagation delay after the control plane edits the list.
const globalAutomaticExclusionTTL = time.Minute

// GlobalAutomaticExclusionStore reads the deployment-wide list of models the
// control plane has removed from automatic routing.
type GlobalAutomaticExclusionStore interface {
	ListGlobalAutomaticRoutingExclusions(ctx context.Context) (map[string]string, error)
}

// globalAutomaticExclusionCache serves the request path from a bounded snapshot
// so a per-turn read never reaches Postgres. A refresh failure keeps serving the
// previous snapshot: routing around a disabled model matters less than staying
// up, and the next refresh recovers.
type globalAutomaticExclusionCache struct {
	store GlobalAutomaticExclusionStore

	mu          sync.Mutex
	byModel     map[string]string
	refreshedAt time.Time
	loaded      bool
	refreshing  bool
}

func newGlobalAutomaticExclusionCache(store GlobalAutomaticExclusionStore) *globalAutomaticExclusionCache {
	return &globalAutomaticExclusionCache{store: store}
}

// snapshot returns the current disabled-model set keyed by model, with the
// operator's reason as the value. A cold cache whose first read fails returns
// nil (fail open) rather than disabling routing on a control-plane outage.
func (c *globalAutomaticExclusionCache) snapshot(ctx context.Context) map[string]string {
	c.mu.Lock()
	if c.loaded && time.Since(c.refreshedAt) < globalAutomaticExclusionTTL {
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
	c.refreshedAt = time.Now()
	c.loaded = true
	c.mu.Unlock()
	return byModel
}

// mergeExcludedModels returns the union of hard and automatic exclusions.
func mergeExcludedModels(hard, automatic map[string]struct{}) map[string]struct{} {
	if len(automatic) == 0 {
		return hard
	}
	merged := make(map[string]struct{}, len(hard)+len(automatic))
	for model := range hard {
		merged[model] = struct{}{}
	}
	for model := range automatic {
		merged[model] = struct{}{}
	}
	return merged
}

// globalAutomaticExcludedModels returns the deployment-wide soft exclusion set
// for this request, or nil when nothing is disabled.
func (s *Service) globalAutomaticExcludedModels(ctx context.Context) map[string]struct{} {
	if s.globalAutomaticExclusions == nil {
		return nil
	}
	byModel := s.globalAutomaticExclusions.snapshot(ctx)
	if len(byModel) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(byModel))
	for model := range byModel {
		out[model] = struct{}{}
	}
	return out
}

// globalAutomaticExclusionReason returns the operator's note for a disabled
// model, so a dropped automatic pin says why in the logs.
func (s *Service) globalAutomaticExclusionReason(ctx context.Context, model string) (string, bool) {
	if s.globalAutomaticExclusions == nil {
		return "", false
	}
	reason, disabled := s.globalAutomaticExclusions.snapshot(ctx)[model]
	return reason, disabled
}
