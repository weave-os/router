package policyregistry

import (
	"context"
	"errors"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/hmm/armid"
)

type servingSnapshotContextKey struct{}

const servingRuntimeCacheSize = 128

// WithServingAssertion derives attribution and isolated state keys from verified admission.
func WithServingAssertion(ctx context.Context, assertion ServingAssertion) context.Context {
	admission := assertion.Admission
	namespace, _ := CanonicalBytes(struct {
		Scope      AdmissionScope
		Activation string
		Release    string
		Generation int64
	}{assertion.Scope, admission.ActivationID, admission.Selection.Release.SHA256, admission.BindingGeneration})
	identity := requestcontext.ServingIdentity{
		Target: string(admission.Target), ActivationID: admission.ActivationID,
		ReleaseID: admission.Selection.Release.SHA256, BindingID: admission.Selection.Binding.SHA256,
		ProfileKey: admission.ProfileKey, ProfileName: admission.ProfileName, Plan: string(admission.Plan), EntitlementVersion: admission.EntitlementVersion,
		BindingGeneration:  admission.BindingGeneration,
		StateNamespace:     Digest(namespace),
		CredentialIdentity: assertion.Scope.CredentialIdentity,
	}
	// Shared credentials retain legacy preference key bytes. Personal subjects
	// survive key rotation but must not share preferences across installations.
	if assertion.Scope.CredentialIdentity != assertion.APIKeyID {
		credentialScope, _ := CanonicalBytes(struct {
			Installation string
			Subject      string
		}{assertion.Scope.InstallationID, assertion.Scope.CredentialIdentity})
		identity.CredentialIdentity = "serving_subject:" + Digest(credentialScope)
	}
	if admission.Selection.Profile != nil {
		identity.ProfileRevision = admission.Selection.Profile.SHA256
	}
	if !assertion.Scope.Persistent {
		identity.StateNamespace = uuid.NewString()
	}
	return requestcontext.WithServingIdentity(ctx, identity)
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
	loaded  *lru.Cache[servingSnapshotKey, *Snapshot]
}

type servingSnapshotKey struct {
	Target     ServingTarget
	ProfileKey string
	Release    ObjectRef
	Binding    ObjectRef
	Profile    ObjectRef
	HasProfile bool
}

// NewServingRuntimeCache keeps independently validated snapshots for concurrent old and new sessions.
func NewServingRuntimeCache(store ServingStore, builder Builder) (*ServingRuntimeCache, error) {
	if store == nil || builder == nil {
		return nil, errors.New("serving runtime cache requires a store and snapshot builder")
	}
	loaded, err := lru.New[servingSnapshotKey, *Snapshot](servingRuntimeCacheSize)
	if err != nil {
		return nil, err
	}
	// Eviction drops only cache ownership: requests keep their snapshot, and later
	// admissions rebuild the exact immutable selection. Idle HTTP connections time out.
	return &ServingRuntimeCache{store: store, builder: builder, loaded: loaded}, nil
}

// Snapshot loads or reuses the immutable runtime for one admission binding.
func (c *ServingRuntimeCache) Snapshot(ctx context.Context, admission SessionReleaseBinding) (*Snapshot, error) {
	if c == nil {
		return nil, errors.New("serving runtime cache is required for managed admission")
	}
	key := servingSnapshotKey{Target: admission.Target, ProfileKey: admission.ProfileKey, Release: admission.Selection.Release, Binding: admission.Selection.Binding}
	if admission.Selection.Profile != nil {
		key.Profile = *admission.Selection.Profile
		key.HasProfile = true
	}
	if snapshot, exists := c.loaded.Get(key); exists {
		return snapshot, nil
	}
	prepared, err := ReadPreparedSelection(ctx, c.store, admission.Target, admission.ProfileKey, admission.Selection)
	if err != nil {
		return nil, err
	}
	if diagnostics := armid.ValidateRosterIDs(prepared.Policy.AllArms()); len(diagnostics) != 0 {
		return nil, errors.New("admitted policy contains arms absent from the worker catalog")
	}
	candidate := Candidate{
		HeadSnapshot: HeadSnapshot{
			// Binding generations are request-scoped, not properties of cached bytes.
			Generation: 0,
			Head: LaneHead{
				ReleaseURI:            admission.Selection.Release.URI,
				ReleaseSHA256:         admission.Selection.Release.SHA256,
				ReleaseGeneration:     admission.Selection.Release.Generation,
				ClassifierRevisionURL: prepared.Binding.Classifier.URL,
			},
		},
		Release: Release{
			Classifier: prepared.Candidate.Classifier.Identity,
			Policy:     prepared.Candidate.Policy,
		},
		Policy:             prepared.Policy,
		ClassifierAudience: prepared.Binding.Classifier.Audience,
	}
	routers, err := c.builder(ctx, candidate)
	if err != nil {
		return nil, err
	}
	if len(routers) == 0 {
		return nil, errors.New("admitted serving snapshot has no routers")
	}
	snapshot := &Snapshot{Candidate: candidate, Routers: routers}
	if existing, loaded, _ := c.loaded.PeekOrAdd(key, snapshot); loaded {
		return existing, nil
	}
	return snapshot, nil
}
