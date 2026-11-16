package proxy

import (
	"context"
	"sync"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/routingpolicy"
)

const globalAutomaticExclusionTTL = time.Minute

type GlobalAutomaticExclusionStore interface {
	ListGlobalAutomaticRoutingExclusions(ctx context.Context) (map[string]string, error)
}

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

func (s *Service) globalAutomaticExcludedModels(ctx context.Context) map[string]struct{} {
	controlPlane := s.controlPlaneExcludedModels(ctx)
	overloaded := s.overloadOpenModels(ctx)
	if len(overloaded) == 0 {
		return controlPlane
	}
	return mergeExcludedModels(overloaded, controlPlane)
}

func (s *Service) controlPlaneExcludedModels(ctx context.Context) map[string]struct{} {
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

func (s *Service) globalAutomaticExclusionReason(ctx context.Context, model string) (string, bool) {
	if s.globalAutomaticExclusions != nil {
		if reason, disabled := s.globalAutomaticExclusions.snapshot(ctx)[model]; disabled {
			return reason, true
		}
	}
	if _, open := s.overloadOpenModels(ctx)[model]; open {
		return overloadBreakerReason, true
	}
	return "", false
}

func modelSet(ids []string) map[string]struct{} { return routingpolicy.ModelSet(ids) }
