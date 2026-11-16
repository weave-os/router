package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

type RoutingPolicyMode string

const (
	RoutingPolicyInherit     RoutingPolicyMode = "inherit"
	RoutingPolicyPassthrough RoutingPolicyMode = "passthrough"
	RoutingPolicyAssigned    RoutingPolicyMode = "assigned"
)

var ErrRoutingPolicyUnavailable = errors.New("routing policy unavailable")

type RoutingPolicy struct {
	Mode     RoutingPolicyMode
	Revision int64
}

type RoutingPolicyRepository interface {
	GetPolicy(context.Context, string) (RoutingPolicy, error)
	HasAssignment(context.Context, string, string, int64) (bool, error)
}

type routingPolicyContextKey struct{}
type routingPolicyGenerationContextKey struct{}
type routingDecisionContextKey struct{}

func RoutingPolicyFrom(ctx context.Context) RoutingPolicy {
	policy, _ := ctx.Value(routingPolicyContextKey{}).(RoutingPolicy)
	return policy
}

func WithRoutingPolicy(ctx context.Context, policy RoutingPolicy) context.Context {
	return context.WithValue(ctx, routingPolicyContextKey{}, policy)
}

type clientModelPassthroughContextKey struct{}

func WithClientModelPassthrough(ctx context.Context) context.Context {
	return context.WithValue(ctx, clientModelPassthroughContextKey{}, true)
}

func WithoutClientModelPassthrough(ctx context.Context) context.Context {
	return context.WithValue(ctx, clientModelPassthroughContextKey{}, false)
}

func ClientModelPassthroughFrom(ctx context.Context) bool {
	selected, _ := ctx.Value(clientModelPassthroughContextKey{}).(bool)
	if !selected {
		return false
	}
	if mode := RoutingPolicyFrom(ctx).Mode; mode == RoutingPolicyPassthrough || mode == RoutingPolicyAssigned {
		return true
	}
	state, active := BlindExperimentFrom(ctx)
	return !active || state.Arm != BlindExperimentArmRouterOn
}

func RoutingPassthroughFrom(ctx context.Context) bool {
	if ClientModelPassthroughFrom(ctx) {
		return true
	}
	policy := RoutingPolicyFrom(ctx)
	if policy.Mode == RoutingPolicyPassthrough {
		return true
	}
	if policy.Mode != RoutingPolicyAssigned {
		return false
	}
	routerOn, _ := ctx.Value(routingDecisionContextKey{}).(bool)
	return !routerOn
}

type policyCacheEntry struct {
	policy     RoutingPolicy
	generation uint64
}

type assignmentCacheEntry struct {
	routerOn   bool
	generation uint64
}

type RoutingPolicyCache struct {
	mu             sync.Mutex
	policies       *expirable.LRU[string, policyCacheEntry]
	assignments    *expirable.LRU[string, assignmentCacheEntry]
	generations    *expirable.LRU[string, uint64]
	nextGeneration uint64
}

func NewRoutingPolicyCache(ttl time.Duration) *RoutingPolicyCache {
	return &RoutingPolicyCache{
		policies:    expirable.NewLRU[string, policyCacheEntry](10000, nil, ttl),
		assignments: expirable.NewLRU[string, assignmentCacheEntry](50000, nil, ttl),
		generations: expirable.NewLRU[string, uint64](10000, nil, ttl),
	}
}

func (cache *RoutingPolicyCache) currentGeneration(installationID string) uint64 {
	if generation, found := cache.generations.Get(installationID); found {
		return generation
	}
	cache.nextGeneration++
	cache.generations.Add(installationID, cache.nextGeneration)
	return cache.nextGeneration
}

func (cache *RoutingPolicyCache) generation(installationID string) uint64 {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.currentGeneration(installationID)
}

func (cache *RoutingPolicyCache) policy(installationID string, generation uint64) (RoutingPolicy, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, found := cache.policies.Get(installationID)
	return entry.policy, found && entry.generation == generation && cache.currentGeneration(installationID) == generation
}

func (cache *RoutingPolicyCache) setPolicy(installationID string, generation uint64, policy RoutingPolicy) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.currentGeneration(installationID) != generation {
		return false
	}
	cache.policies.Add(installationID, policyCacheEntry{policy: policy, generation: generation})
	return true
}

func assignmentKey(installationID, routerUserID string, revision int64) string {
	return fmt.Sprintf("%s\x00%s\x00%d", installationID, routerUserID, revision)
}

func (cache *RoutingPolicyCache) assignment(installationID, routerUserID string, revision int64, generation uint64) (bool, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, found := cache.assignments.Get(assignmentKey(installationID, routerUserID, revision))
	return entry.routerOn, found && entry.generation == generation && cache.currentGeneration(installationID) == generation
}

func (cache *RoutingPolicyCache) setAssignment(installationID, routerUserID string, revision int64, generation uint64, routerOn bool) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.currentGeneration(installationID) != generation {
		return false
	}
	cache.assignments.Add(assignmentKey(installationID, routerUserID, revision), assignmentCacheEntry{routerOn: routerOn, generation: generation})
	return true
}

func (cache *RoutingPolicyCache) InvalidateInstallation(installationID string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.generations.Remove(installationID)
	cache.policies.Remove(installationID)
}
