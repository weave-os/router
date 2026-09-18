package policyregistry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"weave-os/router/internal/router/hmm/rosterdata"
)

// ServingStore is shared by admission, proposal validation and the single activation controller.
type ServingStore interface {
	RootURI() string
	ReadServingObject(context.Context, ServingKind, ObjectRef) (ServingManifest, error)
	ReadServingPolicy(context.Context, ObjectRef) (*rosterdata.Roster, error)
	ReadServingState(context.Context, ServingTarget) (ServingStateSnapshot, error)
	CompareAndSwapServingState(context.Context, ServingControlState, int64) (ServingStateSnapshot, error)
}

// PreparedSelection contains the complete independently validated effective tuple.
type PreparedSelection struct {
	Release    ServingRelease
	Classifier ClassifierBundle
	Binding    DeploymentBinding
	Policy     *rosterdata.Roster
}

// ServingValidator verifies exact revision attestations, catalog compatibility and private endpoint smoke.
// It must not use the controller binary's catalog as a substitute for the selected worker's catalog.
type ServingValidator interface {
	ValidatePreparedSelection(context.Context, PreparedSelection, []ObjectRef) error
}

// ServingController is the only managed activation writer; preparation never calls Activate.
type ServingController struct {
	store     ServingStore
	validator ServingValidator
	clock     func() time.Time
	logger    *slog.Logger
}

// NewServingController requires explicit validation, time and audit dependencies.
func NewServingController(store ServingStore, validator ServingValidator, clock func() time.Time, logger *slog.Logger) (*ServingController, error) {
	if store == nil || validator == nil || clock == nil || logger == nil {
		return nil, errors.New("serving controller requires store, validator, clock and logger")
	}
	return &ServingController{store: store, validator: validator, clock: clock, logger: logger}, nil
}

func readServing[T ServingManifest](ctx context.Context, store ServingStore, kind ServingKind, ref ObjectRef) (T, error) {
	var zero T
	if err := ValidateServingRef(ref, store.RootURI(), kind); err != nil {
		return zero, err
	}
	manifest, err := store.ReadServingObject(ctx, kind, ref)
	if err != nil {
		return zero, err
	}
	typed, ok := manifest.(T)
	if !ok {
		return zero, errors.New("registry returned the wrong manifest kind")
	}
	if err := typed.Validate(store.RootURI()); err != nil {
		return zero, err
	}
	payload, err := CanonicalBytes(typed)
	if err != nil {
		return zero, err
	}
	if Digest(payload) != ref.SHA256 {
		return zero, errors.New("registry serving manifest digest mismatch")
	}
	return typed, nil
}

// Activate requires approval of this exact proposal; retries never reactivate a superseded result.
func (c *ServingController) Activate(ctx context.Context, proposalRef ObjectRef, workflowActor string, approved bool) (ActivationResult, error) {
	proposal, err := readServing[*DeploymentProposal](ctx, c.store, ServingProposals, proposalRef)
	if err != nil {
		return ActivationResult{}, err
	}
	logger := c.logger.With("target", proposal.Target, "proposal_sha256", proposalRef.SHA256, "operator", proposal.Actor, "workflow_actor", workflowActor)
	if !approved {
		return ActivationResult{}, errors.New("activation requires approval bound to this immutable proposal")
	}
	snapshot, err := c.store.ReadServingState(ctx, proposal.Target)
	if err != nil && !errors.Is(err, ErrNotFound) {
		logger.Error("Failed to read authoritative serving state", "err", err)
		return ActivationResult{}, err
	}
	if errors.Is(err, ErrNotFound) {
		snapshot = ServingStateSnapshot{}
	}
	transition, err := NextServingActivation(snapshot, *proposal, proposalRef, c.store.RootURI(), workflowActor, c.clock().UTC())
	if err != nil || transition.Replayed {
		return transition, err
	}
	if err := c.ValidateProposal(ctx, *proposal); err != nil {
		logger.Error("Serving proposal validation blocked activation", "err", err)
		return ActivationResult{}, err
	}
	// Validation may be slow; supersession starts at activation, not at the beginning of smoke checks.
	transition, err = NextServingActivation(snapshot, *proposal, proposalRef, c.store.RootURI(), workflowActor, c.clock().UTC())
	if err != nil {
		return ActivationResult{}, err
	}
	committed, err := c.store.CompareAndSwapServingState(ctx, transition.Snapshot.State, snapshot.Generation)
	if err != nil {
		logger.Error("Serving activation CAS failed; keep the proposal for outcome reconciliation", "err", err)
		return ActivationResult{}, err
	}
	transition.Snapshot = committed
	logger.Info("Serving activation committed", "activation_id", transition.Activation.ID, "sequence", transition.Activation.Sequence, "generation", committed.Generation)
	return transition, nil
}

// ValidateProposal checks all destination profiles and change scope before activation.
func (c *ServingController) ValidateProposal(ctx context.Context, proposal DeploymentProposal) error {
	if err := proposal.Validate(c.store.RootURI()); err != nil {
		return err
	}
	set, err := readServing[*SelectionSet](ctx, c.store, ServingSelectionSets, proposal.SelectionSet)
	if err != nil {
		return err
	}
	if set.Target != proposal.Target {
		return errors.New("proposal and selection set targets differ")
	}
	base, err := c.validateSelection(ctx, proposal.Target, "", set.Default, proposal.Evidence)
	if err != nil {
		return fmt.Errorf("default selection: %w", err)
	}
	baseBinding, err := readServing[*DeploymentBinding](ctx, c.store, ServingBindings, set.Default.Binding)
	if err != nil {
		return err
	}
	for key, selection := range set.Profiles {
		profileRelease, err := c.validateSelection(ctx, proposal.Target, key, selection, proposal.Evidence)
		if err != nil {
			return fmt.Errorf("profile %q: %w", key, err)
		}
		if profileRelease.RouterImageDigest != base.RouterImageDigest || profileRelease.Classifier != base.Classifier {
			return errors.New("profile must share its lane's worker image and classifier bundle")
		}
		profileBinding, err := readServing[*DeploymentBinding](ctx, c.store, ServingBindings, selection.Binding)
		if err != nil {
			return err
		}
		if profileBinding.Router != baseBinding.Router || profileBinding.Classifier != baseBinding.Classifier {
			return errors.New("profile must reuse its lane's prepared worker and classifier revisions")
		}
	}
	source, err := readServing[*ServingRelease](ctx, c.store, ServingReleases, proposal.SourceRelease)
	if err != nil {
		return err
	}
	if proposal.Scope == ChangeFull && set.Default.Release != proposal.SourceRelease {
		return errors.New("full promotion must reuse the exact selected source composition")
	}
	if proposal.PreviousSelectionSet == nil {
		if proposal.Scope != ChangeFull {
			return errors.New("bootstrap requires a full release proposal")
		}
		return nil
	}
	previous, err := readServing[*SelectionSet](ctx, c.store, ServingSelectionSets, *proposal.PreviousSelectionSet)
	if err != nil {
		return err
	}
	if previous.Target != proposal.Target {
		return errors.New("previous selection set belongs to another target")
	}
	for key, selection := range previous.Profiles {
		next, exists := set.Profiles[key]
		if !exists {
			return errors.New("registered profile keys cannot be removed")
		}
		if proposal.Scope != ChangeProfile && *selection.Profile != *next.Profile {
			return errors.New("base promotion must retain destination profile revisions")
		}
		if (proposal.Scope == ChangeProfile && key != proposal.ProfileKey || proposal.Scope == ChangeRoster) && !sameSelection(selection, next) {
			return errors.New("proposal changes an out-of-scope customer tuple")
		}
	}
	for key := range set.Profiles {
		if _, exists := previous.Profiles[key]; !exists && (proposal.Scope != ChangeProfile || key != proposal.ProfileKey) {
			return errors.New("profile registration requires an explicit named-profile proposal")
		}
	}
	if proposal.Scope == ChangeProfile {
		selection, exists := set.Profiles[proposal.ProfileKey]
		if !exists || !sameSelection(previous.Default, set.Default) {
			return errors.New("profile-only promotion must preserve the default and name a registered profile")
		}
		profileRelease, err := readServing[*ServingRelease](ctx, c.store, ServingReleases, selection.Release)
		if err != nil {
			return err
		}
		if profileRelease.Policy != source.Policy {
			return errors.New("profile promotion does not use the selected source policy")
		}
		return nil
	}
	oldBase, err := readServing[*ServingRelease](ctx, c.store, ServingReleases, previous.Default.Release)
	if err != nil {
		return err
	}
	oldBinding, err := readServing[*DeploymentBinding](ctx, c.store, ServingBindings, previous.Default.Binding)
	if err != nil {
		return err
	}
	if (proposal.Scope == ChangeRoster || proposal.Scope == ChangeClassifier) && baseBinding.Router != oldBinding.Router {
		return errors.New("component-only proposal must reuse the destination worker revision")
	}
	if (proposal.Scope == ChangeRoster || proposal.Scope == ChangeRouter) && baseBinding.Classifier != oldBinding.Classifier {
		return errors.New("component-only proposal must reuse the destination classifier revision")
	}
	switch proposal.Scope {
	case ChangeRouter:
		if base.RouterImageDigest != source.RouterImageDigest || base.Policy != oldBase.Policy || base.Classifier != oldBase.Classifier {
			return errors.New("router-only promotion must retain destination policy and classifier")
		}
	case ChangeRoster:
		if base.Policy != source.Policy || base.RouterImageDigest != oldBase.RouterImageDigest || base.Classifier != oldBase.Classifier {
			return errors.New("roster-only promotion must retain destination image and classifier")
		}
	case ChangeClassifier:
		if base.Classifier != source.Classifier || base.RouterImageDigest != oldBase.RouterImageDigest || base.Policy != oldBase.Policy {
			return errors.New("classifier-only promotion must retain destination image and policy")
		}
	}
	return nil
}

func (c *ServingController) validateSelection(ctx context.Context, target ServingTarget, profileKey string, selection ServingSelection, evidence []ObjectRef) (ServingRelease, error) {
	prepared, err := ReadPreparedSelection(ctx, c.store, target, profileKey, selection)
	if err != nil {
		return ServingRelease{}, err
	}
	if err := c.validator.ValidatePreparedSelection(ctx, prepared, evidence); err != nil {
		return ServingRelease{}, err
	}
	return prepared.Release, nil
}

// ReadPreparedSelection reads and cross-validates one exact tuple without consulting mutable heads.
func ReadPreparedSelection(ctx context.Context, store ServingStore, target ServingTarget, profileKey string, selection ServingSelection) (PreparedSelection, error) {
	if err := selection.validate(store.RootURI(), profileKey != ""); err != nil {
		return PreparedSelection{}, err
	}
	release, err := readServing[*ServingRelease](ctx, store, ServingReleases, selection.Release)
	if err != nil {
		return PreparedSelection{}, err
	}
	binding, err := readServing[*DeploymentBinding](ctx, store, ServingBindings, selection.Binding)
	if err != nil {
		return PreparedSelection{}, err
	}
	bundle, err := readServing[*ClassifierBundle](ctx, store, ServingClassifiers, release.Classifier)
	if err != nil {
		return PreparedSelection{}, err
	}
	if binding.Target != target || binding.Release != selection.Release || binding.Router.ImageDigest != release.RouterImageDigest || binding.Classifier.ImageDigest != bundle.Identity.ImageDigest || binding.ClassifierBundleSHA256 != release.Classifier.SHA256 || binding.Classifier.Configuration != bundle.Configuration {
		return PreparedSelection{}, errors.New("deployment binding differs from release or classifier identity/configuration")
	}
	if release.Requirements.ClassifierWireSchema != bundle.Identity.WireSchema || release.Requirements.TaxonomySHA256 != bundle.Identity.TaxonomySHA256 {
		return PreparedSelection{}, errors.New("release requirements do not match classifier bundle")
	}
	policy, err := store.ReadServingPolicy(ctx, ObjectRef{URI: release.Policy.URI, SHA256: release.Policy.SHA256, Generation: release.Policy.Generation})
	if err != nil {
		return PreparedSelection{}, err
	}
	if policy.SchemaVersion != release.Requirements.PolicySchema || !slices.Equal(policy.ClassOrder, bundle.Identity.ClassOrder) {
		return PreparedSelection{}, errors.New("compiled policy does not match classifier schema/taxonomy")
	}
	if profileKey != "" {
		if selection.Profile == nil {
			return PreparedSelection{}, errors.New("assigned profile has no exact revision")
		}
		profile, err := readServing[*RoutingProfile](ctx, store, ServingProfiles, *selection.Profile)
		if err != nil {
			return PreparedSelection{}, err
		}
		if profile.ProfileKey != profileKey || profile.Policy != release.Policy || profile.Requirements != release.Requirements {
			return PreparedSelection{}, errors.New("effective tuple differs from the assigned profile's immutable policy")
		}
	}
	return PreparedSelection{Release: *release, Classifier: *bundle, Binding: *binding, Policy: policy}, nil
}
