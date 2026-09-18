package policyregistry

import (
	"context"
	"errors"
	"sync"
)

type servingSnapshotContextKey struct{}

type servingAssertionContextKey struct{}

// WithServingAssertion stores verified gateway admission on the worker request.
func WithServingAssertion(ctx context.Context, assertion ServingAssertion) context.Context {
	return context.WithValue(ctx, servingAssertionContextKey{}, assertion)
}

// ServingAssertionFromContext returns verified admission when the worker is serving managed traffic.
func ServingAssertionFromContext(ctx context.Context) (ServingAssertion, bool) {
	assertion, ok := ctx.Value(servingAssertionContextKey{}).(ServingAssertion)
	return assertion, ok
}

// WithServingSnapshot pins every routing/outcome/feedback call on one admitted runtime.
func WithServingSnapshot(ctx context.Context, snapshot *Snapshot) context.Context {
	if snapshot == nil {
		return ctx
	}
	return context.WithValue(ctx, servingSnapshotContextKey{}, snapshot)
}

// ServingSnapshotFromContext returns the request-bound snapshot, if any.
func ServingSnapshotFromContext(ctx context.Context) *Snapshot {
	snapshot, _ := ctx.Value(servingSnapshotContextKey{}).(*Snapshot)
	return snapshot
}

// ServingRuntimeCache materializes exact admitted releases instead of the latest lane head.
type ServingRuntimeCache struct {
	store   ServingStore
	builder Builder
	mu      sync.Mutex
	loaded  map[string]*Snapshot
}

// NewServingRuntimeCache keeps independently validated snapshots for concurrent old and new sessions.
func NewServingRuntimeCache(store ServingStore, builder Builder) (*ServingRuntimeCache, error) {
	if store == nil || builder == nil {
		return nil, errors.New("serving runtime cache requires a store and snapshot builder")
	}
	return &ServingRuntimeCache{store: store, builder: builder, loaded: map[string]*Snapshot{}}, nil
}

// Snapshot loads or reuses the immutable runtime for one admission binding.
func (c *ServingRuntimeCache) Snapshot(ctx context.Context, admission SessionReleaseBinding) (*Snapshot, error) {
	if c == nil {
		return nil, errors.New("serving runtime cache is required for managed admission")
	}
	key := admission.Selection.Release.SHA256 + ":" + admission.Selection.Binding.SHA256
	if admission.Selection.Profile != nil {
		key += ":" + admission.Selection.Profile.SHA256
	}
	c.mu.Lock()
	if snapshot := c.loaded[key]; snapshot != nil {
		c.mu.Unlock()
		return snapshot, nil
	}
	c.mu.Unlock()
	prepared, err := ReadPreparedSelection(ctx, c.store, admission.Target, admission.ProfileKey, admission.Selection)
	if err != nil {
		return nil, err
	}
	candidate := Candidate{
		HeadSnapshot: HeadSnapshot{
			Generation: admission.BindingGeneration,
			Head: LaneHead{
				ReleaseURI:            admission.Selection.Release.URI,
				ReleaseSHA256:         admission.Selection.Release.SHA256,
				ReleaseGeneration:     admission.Selection.Release.Generation,
				ClassifierRevisionURL: prepared.Binding.Classifier.URL,
			},
		},
		Release: Release{
			Classifier: prepared.Classifier.Identity,
			Policy:     prepared.Release.Policy,
		},
		Policy: prepared.Policy,
	}
	routers, err := c.builder(ctx, candidate)
	if err != nil {
		return nil, err
	}
	if len(routers) == 0 {
		return nil, errors.New("admitted serving snapshot has no routers")
	}
	snapshot := &Snapshot{Candidate: candidate, Routers: routers}
	c.mu.Lock()
	c.loaded[key] = snapshot
	c.mu.Unlock()
	return snapshot, nil
}
