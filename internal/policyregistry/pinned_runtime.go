package policyregistry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"weave-os/router/internal/router"
)

// PinnedRuntimeRequest loads one immutable V3 policy into a local transport.
// The caller retains tenant/credential admission; this is not fleet authorization.
type PinnedRuntimeRequest struct {
	Target                  ServingTarget
	ProfileKey              string
	SelectionSet            ObjectRef
	RouterRevision          string
	PolicySHA256            string
	ClassifierArtifactID    string
	ClassifierPackageSHA256 string
	ClassifierImageDigest   string
	ClassifierURL           string
}

type PinnedRuntime struct {
	Snapshot   *Snapshot
	Selection  ServingSelection
	Target     ServingTarget
	ProfileKey string
}

// Bind preserves the exact loaded snapshot across route, outcome and feedback calls.
// It deliberately does not fabricate a managed admission assertion or activation.
func (p *PinnedRuntime) Bind(ctx context.Context) context.Context {
	return WithServingSnapshot(ctx, p.Snapshot)
}

// LoadPinnedRuntime reads immutable objects only; mutable heads and activation writes
// are never consulted. Classifier transport may change, classifier identity may not.
func LoadPinnedRuntime(ctx context.Context, store ServingStore, pin PinnedRuntimeRequest, build Builder) (*PinnedRuntime, error) {
	if store == nil || build == nil {
		return nil, errors.New("pinned runtime requires registry and builder")
	}
	if err := ValidateServingRef(pin.SelectionSet, store.RootURI(), ServingSelectionSet); err != nil {
		return nil, err
	}
	if !sourceRevisionPattern.MatchString(pin.RouterRevision) || !validDigest(pin.PolicySHA256) || !validDigest(pin.ClassifierPackageSHA256) || pin.ClassifierArtifactID == "" {
		return nil, errors.New("pinned runtime requires exact source, policy and classifier identities")
	}
	endpoint, err := url.Parse(pin.ClassifierURL)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("local classifier must be an HTTP loopback origin")
	}
	host := endpoint.Hostname()
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
		return nil, errors.New("local classifier requires loopback and an explicit port")
	}
	manifest, _, err := store.ReadServingObject(ctx, ServingSelectionSet, pin.SelectionSet)
	if err != nil {
		return nil, err
	}
	set, ok := manifest.(*SelectionSetV3)
	if !ok || set.Target != pin.Target {
		return nil, errors.New("pinned runtime requires a V3 selection for the declared target")
	}
	view := set.View(pin.SelectionSet)
	selection := view.Default
	// Profile loading retains the V3 binding contract, but the local entrypoint
	// supplies only the default until product-specific admission is available.
	if pin.ProfileKey != "" {
		var found bool
		selection, found = view.Profiles[pin.ProfileKey]
		if !found {
			return nil, errors.New("pinned profile is absent from the selection")
		}
	}
	prepared, err := ReadPreparedSelection(ctx, store, pin.Target, pin.ProfileKey, selection)
	if err != nil {
		return nil, err
	}
	classifier := prepared.Candidate.Classifier.Identity
	if prepared.Candidate.Provenance.RouterRevision != pin.RouterRevision || prepared.PolicyReference.SHA256 != pin.PolicySHA256 || classifier.ArtifactID != pin.ClassifierArtifactID || classifier.PackageSHA256 != pin.ClassifierPackageSHA256 || classifier.ImageDigest != pin.ClassifierImageDigest {
		return nil, errors.New("pinned runtime source, effective policy or classifier differs from requested identities")
	}
	cache, err := NewServingRuntimeCache(store, func(ctx context.Context, candidate Candidate) (map[router.Strategy]router.Router, error) {
		candidate.HeadSnapshot.Head.ClassifierRevisionURL = pin.ClassifierURL
		candidate.ClassifierAudience = ""
		return build(ctx, candidate)
	})
	if err != nil {
		return nil, err
	}
	snapshot, err := cache.Snapshot(ctx, SessionReleaseBinding{Target: pin.Target, ProfileKey: pin.ProfileKey, Selection: selection})
	if err != nil {
		return nil, fmt.Errorf("load pinned runtime: %w", err)
	}
	return &PinnedRuntime{Snapshot: snapshot, Selection: selection, Target: pin.Target, ProfileKey: pin.ProfileKey}, nil
}
