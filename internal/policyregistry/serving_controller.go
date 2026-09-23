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
// ReadServingObject returns the stored object alongside its exact payload bytes; implementations
// must verify the bytes against the reference digest and generation. Stored encodings may predate
// this binary's canonical form.
type ServingStore interface {
	RootURI() string
	ReadServingObject(context.Context, ServingKind, ObjectRef) (ServingManifest, []byte, error)
	ReadServingPolicy(context.Context, ObjectRef) (*rosterdata.Roster, error)
	ReadServingState(context.Context, ServingTarget) (ServingStateSnapshot, error)
	CompareAndSwapServingState(context.Context, ServingControlState, int64) (ServingStateSnapshot, error)
}

// ServingValidationStore additionally verifies audit/provenance artifacts by exact immutable reference.
// Artifact contents are trusted registry-writer evidence, not a controller-defined cryptographic proof format.
type ServingValidationStore interface {
	ServingStore
	VerifyServingArtifact(context.Context, ObjectRef) error
}

// PreparedSelection contains the complete independently validated effective tuple.
type PreparedSelection struct {
	Selection  ServingSelection
	ProfileKey string
	Release    ServingRelease
	Classifier ClassifierBundle
	Binding    DeploymentBinding
	Policy     *rosterdata.Roster
}

// ServingValidator verifies exact revision attestations, catalog compatibility and private endpoint smoke.
// It must not use the controller binary's catalog as a substitute for the selected worker's catalog.
type ServingValidator interface {
	ValidatePreparedSelection(context.Context, PreparedSelection) error
}

// ServingController is the only managed activation writer; preparation never calls Activate.
type ServingController struct {
	store     ServingValidationStore
	validator ServingValidator
	clock     func() time.Time
	logger    *slog.Logger
}

// NewServingController requires explicit validation, time and audit dependencies.
func NewServingController(store ServingValidationStore, validator ServingValidator, clock func() time.Time, logger *slog.Logger) (*ServingController, error) {
	if store == nil || validator == nil || clock == nil || logger == nil {
		return nil, errors.New("serving controller requires store, validator, clock and logger")
	}
	return &ServingController{store: store, validator: validator, clock: clock, logger: logger}, nil
}

func readServing[T ServingManifest](ctx context.Context, store ServingStore, kind ServingKind, ref ObjectRef) (T, error) {
	typed, _, err := readServingPayload[T](ctx, store, kind, ref)
	return typed, err
}

// readServingPayload additionally returns the exact stored payload so callers can bind later
// checks to the immutable bytes instead of this binary's canonical re-encoding.
func readServingPayload[T ServingManifest](ctx context.Context, store ServingStore, kind ServingKind, ref ObjectRef) (T, []byte, error) {
	var zero T
	if err := ValidateServingRef(ref, store.RootURI(), kind); err != nil {
		return zero, nil, err
	}
	manifest, payload, err := store.ReadServingObject(ctx, kind, ref)
	if err != nil {
		return zero, nil, err
	}
	if Digest(payload) != ref.SHA256 {
		return zero, nil, errors.New("registry serving manifest digest mismatch")
	}
	typed, ok := manifest.(T)
	if !ok {
		return zero, nil, errors.New("registry returned the wrong manifest kind")
	}
	if err := typed.Validate(store.RootURI()); err != nil {
		return zero, nil, err
	}
	return typed, payload, nil
}

// PreparationResult distinguishes fresh readiness from an already-activated idempotent outcome.
type PreparationResult struct {
	Proposal   ObjectRef         `json:"proposal"`
	Prepared   bool              `json:"prepared"`
	Activation *ActivationResult `json:"activation,omitempty"`
}

// Prepare validates a frozen proposal without writes; completed retries never require healthy old revisions.
func (c *ServingController) Prepare(ctx context.Context, proposalRef ObjectRef) (PreparationResult, error) {
	logger := c.logger.With("proposal_sha256", proposalRef.SHA256)
	proposal, proposalPayload, err := readServingPayload[*DeploymentProposal](ctx, c.store, ServingProposals, proposalRef)
	if err != nil {
		logger.Error("Failed to read immutable proposal for serving preparation", "err", err)
		return PreparationResult{}, err
	}
	logger = logger.With("target", proposal.Target, "operator", proposal.Actor)
	snapshot, err := c.store.ReadServingState(ctx, proposal.Target)
	if errors.Is(err, ErrNotFound) {
		snapshot = ServingStateSnapshot{}
	} else if err != nil {
		logger.Error("Failed to read authoritative target for serving preparation", "err", err)
		return PreparationResult{}, err
	}
	transition, err := NextServingActivation(snapshot, proposalPayload, proposalRef, c.store.RootURI(), proposal.Actor, c.clock().UTC())
	if err != nil {
		logger.Warn("Serving preparation transition rejected", "expected_generation", proposal.ExpectedGeneration, "generation", snapshot.Generation, "err", err)
		return PreparationResult{}, err
	}
	if transition.Replayed {
		logger.Info("Serving preparation reconciled a previous activation", "activation_id", transition.Activation.ID, "outcome", transition.Outcome)
		return PreparationResult{Proposal: proposalRef, Activation: &transition}, nil
	}
	if len(proposal.WithdrawActivations) > 0 {
		if err := c.validateRollbackSource(ctx, snapshot, *proposal); err != nil {
			logger.Warn("Serving preparation rollback source rejected", "source_release_sha256", proposal.SourceRelease.SHA256, "err", err)
			return PreparationResult{}, err
		}
	}
	if err := c.ValidateProposal(ctx, *proposal); err != nil {
		logger.Error("Serving destination validation blocked preparation", "selection_set_sha256", proposal.SelectionSet.SHA256, "err", err)
		return PreparationResult{}, err
	}
	logger.Info("Serving proposal prepared without activation", "selection_set_sha256", proposal.SelectionSet.SHA256, "expected_generation", proposal.ExpectedGeneration)
	return PreparationResult{Proposal: proposalRef, Prepared: true}, nil
}

// Rollback uses the activation CAS path but only accepts a source previously serving this target.
// Normal rollback retains pins; an approved proposal explicitly lists emergency withdrawals.
func (c *ServingController) Rollback(ctx context.Context, proposalRef ObjectRef, workflowActor string, approved bool) (ActivationResult, error) {
	return c.activate(ctx, proposalRef, workflowActor, approved, true)
}

func (c *ServingController) validateRollbackSource(ctx context.Context, snapshot ServingStateSnapshot, proposal DeploymentProposal) error {
	for _, activation := range snapshot.State.Activations {
		set, err := readServing[*SelectionSet](ctx, c.store, ServingSelectionSets, activation.SelectionSet)
		if err != nil {
			return err
		}
		if set.Target != proposal.Target {
			return errors.New("rollback history belongs to another target")
		}
		selection := set.Default
		if proposal.Scope == ChangeProfile {
			selection = set.Profiles[proposal.ProfileKey]
		}
		if selection.Release == proposal.SourceRelease {
			return nil
		}
	}
	return errors.New("rollback requires a known-good source release previously serving the same target and profile")
}

// Activate requires approval of this exact proposal; retries never reactivate a superseded result.
func (c *ServingController) Activate(ctx context.Context, proposalRef ObjectRef, workflowActor string, approved bool) (ActivationResult, error) {
	return c.activate(ctx, proposalRef, workflowActor, approved, false)
}

func (c *ServingController) activate(ctx context.Context, proposalRef ObjectRef, workflowActor string, approved, rollback bool) (ActivationResult, error) {
	logger := c.logger.With("proposal_sha256", proposalRef.SHA256, "workflow_actor", workflowActor)
	proposal, proposalPayload, err := readServingPayload[*DeploymentProposal](ctx, c.store, ServingProposals, proposalRef)
	if err != nil {
		logger.Error("Failed to read immutable serving activation proposal", "err", err)
		return ActivationResult{}, err
	}
	logger = logger.With("target", proposal.Target, "operator", proposal.Actor)
	if !approved {
		logger.Warn("Serving activation rejected: proposal approval missing")
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
	transition, err := NextServingActivation(snapshot, proposalPayload, proposalRef, c.store.RootURI(), workflowActor, c.clock().UTC())
	if err != nil {
		logger.Warn("Serving activation transition rejected", "expected_generation", proposal.ExpectedGeneration, "generation", snapshot.Generation, "err", err)
		return transition, err
	}
	if transition.Replayed {
		logger.Info("Serving activation reconciled a previous outcome", "activation_id", transition.Activation.ID, "outcome", transition.Outcome)
		return transition, nil
	}
	if rollback || len(proposal.WithdrawActivations) > 0 {
		if err := c.validateRollbackSource(ctx, snapshot, *proposal); err != nil {
			logger.Warn("Serving rollback source validation rejected", "source_release_sha256", proposal.SourceRelease.SHA256, "err", err)
			return ActivationResult{}, err
		}
	}
	if err := c.ValidateProposal(ctx, *proposal); err != nil {
		logger.Error("Serving proposal validation blocked activation", "err", err)
		return ActivationResult{}, err
	}
	// Validation may be slow; supersession starts at activation, not at the beginning of smoke checks.
	transition, err = NextServingActivation(snapshot, proposalPayload, proposalRef, c.store.RootURI(), workflowActor, c.clock().UTC())
	if err != nil {
		logger.Warn("Serving activation transition construction rejected", "err", err)
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
	for _, evidence := range proposal.Evidence {
		if err := c.store.VerifyServingArtifact(ctx, evidence); err != nil {
			return fmt.Errorf("verify proposal evidence: %w", err)
		}
	}
	set, err := readServing[*SelectionSet](ctx, c.store, ServingSelectionSets, proposal.SelectionSet)
	if err != nil {
		return err
	}
	if set.Target != proposal.Target {
		return errors.New("proposal and selection set targets differ")
	}
	base, err := c.validateSelection(ctx, proposal.Target, "", set.Default)
	if err != nil {
		return fmt.Errorf("default selection: %w", err)
	}
	baseBinding, err := readServing[*DeploymentBinding](ctx, c.store, ServingBindings, set.Default.Binding)
	if err != nil {
		return err
	}
	for key, selection := range set.Profiles {
		profileRelease, err := c.validateSelection(ctx, proposal.Target, key, selection)
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
	if err := c.store.VerifyServingArtifact(ctx, source.Provenance.BuildAttestation); err != nil {
		return fmt.Errorf("verify source build attestation: %w", err)
	}
	if (proposal.Scope == ChangeFull || proposal.Scope == ChangeRollback) && set.Default.Release != proposal.SourceRelease {
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
	if proposal.Scope == ChangeRollback {
		// Only the exact historical object may bypass forward profile preservation.
		snapshot, err := c.store.ReadServingState(ctx, proposal.Target)
		if err != nil {
			c.logger.Error("Failed to read exact rollback history", "target", proposal.Target, "selection_set_sha256", proposal.SelectionSet.SHA256, "err", err)
			return fmt.Errorf("read exact rollback history: %w", err)
		}
		for _, activation := range snapshot.State.Activations {
			if activation.SelectionSet == proposal.SelectionSet {
				return nil
			}
		}
		c.logger.Warn("Rejected exact rollback: selection set was not previously activated on target", "target", proposal.Target, "selection_set_sha256", proposal.SelectionSet.SHA256)
		return errors.New("exact rollback requires a selection set previously activated on the same target")
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
		if err := c.validateRouterOnlyLanes(ctx, set, previous, *source); err != nil {
			c.logger.Warn("Rejected router-only proposal", "target", proposal.Target, "selection_set_sha256", proposal.SelectionSet.SHA256, "err", err)
			return err
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

// validateRouterOnlyLanes admits a Router-only update only when Default and every predecessor
// profile move together: each lane receives a new release and binding that differ from their
// predecessors solely by the source Router image, and every successor lane shares one new Router
// revision. Runs after structural selection validation and forward profile preservation.
func (c *ServingController) validateRouterOnlyLanes(ctx context.Context, next, previous *SelectionSet, source ServingRelease) error {
	if len(next.Profiles) != len(previous.Profiles) {
		return errors.New("router-only promotion must retain the destination profile inventory")
	}
	previousDefaultBinding, err := readServing[*DeploymentBinding](ctx, c.store, ServingBindings, previous.Default.Binding)
	if err != nil {
		return fmt.Errorf("read predecessor default binding: %w", err)
	}
	nextDefaultBinding, err := readServing[*DeploymentBinding](ctx, c.store, ServingBindings, next.Default.Binding)
	if err != nil {
		return fmt.Errorf("read successor default binding: %w", err)
	}
	predecessorRouterConfiguration := previousDefaultBinding.Router.Configuration
	sharedRouter := nextDefaultBinding.Router
	if err := c.validateRouterOnlyLane(ctx, previous.Default, next.Default, source, predecessorRouterConfiguration, sharedRouter); err != nil {
		return fmt.Errorf("default lane: %w", err)
	}
	for key, previousSelection := range previous.Profiles {
		nextSelection, exists := next.Profiles[key]
		if !exists {
			return errors.New("registered profile keys cannot be removed")
		}
		if err := c.validateRouterOnlyLane(ctx, previousSelection, nextSelection, source, predecessorRouterConfiguration, sharedRouter); err != nil {
			return fmt.Errorf("profile %s lane: %w", key, err)
		}
	}
	return nil
}

// validateRouterOnlyLane compares one predecessor/successor lane pair. The successor release may
// differ only in Router image digest and provenance; the successor binding may differ only in
// release, attestation, and the shared Router revision.
func (c *ServingController) validateRouterOnlyLane(ctx context.Context, previous, next ServingSelection, source ServingRelease, predecessorRouterConfiguration ObjectRef, sharedRouter RevisionBinding) error {
	if next.Release == previous.Release || next.Binding == previous.Binding {
		return errors.New("router-only promotion must publish a new release and binding for every lane")
	}
	previousRelease, err := readServing[*ServingRelease](ctx, c.store, ServingReleases, previous.Release)
	if err != nil {
		return fmt.Errorf("read predecessor release: %w", err)
	}
	nextRelease, err := readServing[*ServingRelease](ctx, c.store, ServingReleases, next.Release)
	if err != nil {
		return fmt.Errorf("read successor release: %w", err)
	}
	previousBinding, err := readServing[*DeploymentBinding](ctx, c.store, ServingBindings, previous.Binding)
	if err != nil {
		return fmt.Errorf("read predecessor binding: %w", err)
	}
	nextBinding, err := readServing[*DeploymentBinding](ctx, c.store, ServingBindings, next.Binding)
	if err != nil {
		return fmt.Errorf("read successor binding: %w", err)
	}
	if previousBinding.Router.Configuration != predecessorRouterConfiguration {
		return errors.New("router-only promotion requires an identical predecessor Router configuration across lanes")
	}
	if nextRelease.RouterImageDigest != source.RouterImageDigest || nextRelease.Provenance != source.Provenance {
		return errors.New("router-only promotion must carry the source Router image and provenance")
	}
	expectedRelease := *previousRelease
	expectedRelease.RouterImageDigest = nextRelease.RouterImageDigest
	expectedRelease.Provenance = nextRelease.Provenance
	if expectedRelease != *nextRelease {
		return errors.New("router-only promotion must retain destination policy, classifier, and requirements")
	}
	if nextBinding.Router == previousBinding.Router || nextBinding.Router != sharedRouter {
		return errors.New("router-only promotion must share one new Router revision across every lane")
	}
	expectedBinding := *previousBinding
	expectedBinding.Release = nextBinding.Release
	expectedBinding.Attestation = nextBinding.Attestation
	expectedBinding.Router = nextBinding.Router
	if expectedBinding != *nextBinding {
		return errors.New("router-only promotion must retain destination target, project, region, and classifier revision")
	}
	return nil
}

func (c *ServingController) validateSelection(ctx context.Context, target ServingTarget, profileKey string, selection ServingSelection) (ServingRelease, error) {
	prepared, err := ReadPreparedSelection(ctx, c.store, target, profileKey, selection)
	if err != nil {
		return ServingRelease{}, err
	}
	if err := c.store.VerifyServingArtifact(ctx, prepared.Release.Provenance.BuildAttestation); err != nil {
		return ServingRelease{}, fmt.Errorf("verify destination build attestation: %w", err)
	}
	if err := c.store.VerifyServingArtifact(ctx, prepared.Binding.Attestation); err != nil {
		return ServingRelease{}, fmt.Errorf("verify physical revision attestation: %w", err)
	}
	if err := c.validator.ValidatePreparedSelection(ctx, prepared); err != nil {
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
	return PreparedSelection{Selection: selection, ProfileKey: profileKey, Release: *release, Classifier: *bundle, Binding: *binding, Policy: policy}, nil
}
