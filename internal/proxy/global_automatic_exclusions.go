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
}

func newGlobalAutomaticExclusionCache(store GlobalAutomaticExclusionStore) *globalAutomaticExclusionCache {
	return &globalAutomaticExclusionCache{store: store}
}

// snapshot returns the current disabled-model set keyed by model, with the
// operator's reason as the value. A cold cache whose first read fails returns
// nil (fail open) rather than disabling routing on a control-plane outage.
func (c *globalAutomaticExclusionCache) snapshot(ctx context.Context) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && time.Since(c.refreshedAt) < globalAutomaticExclusionTTL {
		return c.byModel
	}
	byModel, err := c.store.ListGlobalAutomaticRoutingExclusions(ctx)
	if err != nil {
		observability.FromContext(ctx).Error("Failed to refresh global automatic-routing exclusions",
			"err", err,
			"serving_stale_snapshot", c.loaded,
			"cached_model_count", len(c.byModel),
		)
		// Hold the stale timestamp so a persistent failure retries every turn
		// rather than pinning an empty set for a full TTL.
		return c.byModel
	}
	c.byModel = byModel
	c.refreshedAt = time.Now()
	c.loaded = true
	return byModel
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
