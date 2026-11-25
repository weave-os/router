package policyregistry

import (
	"context"
	"errors"
	"maps"
	"slices"

	"strings"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

type servingSnapshotContextKey struct{}
type servingAssertionContextKey struct{}

func ServingAssertionFromContext(ctx context.Context) *ServingAssertion {
	assertion, _ := ctx.Value(servingAssertionContextKey{}).(*ServingAssertion)
	return assertion
}

const servingRuntimeCacheSize = 128

func WithServingAssertion(ctx context.Context, assertion ServingAssertion) context.Context {
	if assertion.TestPlan != nil {
		ctx = context.WithValue(ctx, servingAssertionContextKey{}, &assertion)
		ctx = requestcontext.WithInternalTestIdentity(ctx, requestcontext.InternalTestIdentity{SubjectID: assertion.TestPlan.SubjectID, SessionID: assertion.TestPlan.SessionID})
	}
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

func WithServingRecipe(ctx context.Context, worker WorkerIdentity, snapshot *Snapshot) context.Context {
	identity, managed := requestcontext.ServingIdentityFromContext(ctx)
	if !managed {
		return ctx
	}
	identity.Recipe = requestcontext.ServingRecipe{
		WorkerRevision:            worker.Revision,
		WorkerImageDigest:         worker.ImageDigest,
		WorkerConfigurationSHA256: worker.Configuration.SHA256,
	}
	if snapshot != nil {
		identity.Recipe.PolicySHA256 = snapshot.Release.Policy.SHA256
		identity.Recipe.ClassifierReleaseID = snapshot.ClassifierReleaseID
		identity.Recipe.ClassifierEnabledVariants = joinClassifierVariants(snapshot.ClassifierEnabledVariants)
	}
	return requestcontext.WithServingIdentity(ctx, identity)
}

func WithServingSnapshot(ctx context.Context, snapshot *Snapshot) context.Context {
	if snapshot == nil {
		return ctx
	}
	return context.WithValue(ctx, servingSnapshotContextKey{}, snapshot)
}

func ServingSnapshotFromContext(ctx context.Context) *Snapshot {
	snapshot, _ := ctx.Value(servingSnapshotContextKey{}).(*Snapshot)
	return snapshot
}

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

func NewServingRuntimeCache(store ServingStore, builder Builder) (*ServingRuntimeCache, error) {
	if store == nil || builder == nil {
		return nil, errors.New("serving runtime cache requires a store and snapshot builder")
	}
	loaded, err := lru.New[servingSnapshotKey, *Snapshot](servingRuntimeCacheSize)
	if err != nil {
		return nil, err
	}
	return &ServingRuntimeCache{store: store, builder: builder, loaded: loaded}, nil
}

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
	if diagnostics := prepared.Policy.CatalogDiagnostics(); len(diagnostics) != 0 {
		return nil, errors.New("admitted policy contains arms absent from the worker catalog")
	}
	candidate := Candidate{
		AuxiliaryModels: maps.Clone(prepared.Candidate.Classifier.AuxiliaryModels),
		HeadSnapshot: HeadSnapshot{
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
			Policy:     prepared.PolicyReference,
		},
		Policy:             prepared.Policy,
		ClassifierAudience: prepared.Binding.Classifier.Audience,
	}
	if prepared.ClassifierServiceBinding != nil {
		if prepared.ClassifierRuntimeBinding == nil {
			return nil, errors.New("v4 selection has no classifier runtime binding")
		}
		candidate.ClassifierReleaseV5 = prepared.ClassifierReleaseV5
		candidate.ClassifierReleaseID = prepared.ClassifierServiceBinding.Release.SHA256
		candidate.ClassifierSelectionID = admission.Selection.Binding.SHA256
		candidate.ClassifierWorkerURL = prepared.ClassifierRuntimeBinding.Binding.WorkerURL
		candidate.ClassifierWorkerAudience = prepared.ClassifierRuntimeBinding.Binding.WorkerAudience
		candidate.ClassifierEnabledVariants = slices.Clone(prepared.ClassifierServiceBinding.EnabledVariants)
		candidate.SupportedAPIMajors = slices.Clone(prepared.Candidate.SupportedAPIMajors)
	}
	candidate.StrategyPolicies = prepared.StrategyPolicies
	routers, err := c.builder(ctx, candidate)
	if err != nil {
		return nil, err
	}
	if len(routers) == 0 {
		return nil, errors.New("admitted serving snapshot has no routers")
	}
	if routers[router.PrimaryRoutingStrategy] == nil {
		return nil, errors.New("admitted serving snapshot has no primary classifier router")
	}
	snapshot := &Snapshot{Candidate: candidate, Routers: routers}
	if existing, loaded, _ := c.loaded.PeekOrAdd(key, snapshot); loaded {
		return existing, nil
	}
	return snapshot, nil
}

func joinClassifierVariants(variants []policy.ClassifierVariantID) string {
	names := make([]string, 0, len(variants))
	for _, variant := range variants {
		names = append(names, string(variant))
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}
