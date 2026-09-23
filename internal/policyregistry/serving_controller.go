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

// PreparedSelection contains the complete independently validated effective tuple. It is the
// same DTO whether the selection was stored as v1 release/classifier/binding/profile objects or
// as a lane embedded in a v2 selection set.
type PreparedSelection struct {
	Selection  ServingSelection
	ProfileKey string
	Target     ServingTarget
	Candidate  CandidateComposition
	Binding    LaneBinding
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

// readServingPayload additionally returns the exact stored payload. Reads trust the
// generation-pinned reference; only the proposal anchor re-verifies its digest, inside
// NextServingActivation, because replay and status are keyed on the recorded proposal ref.
func readServingPayload[T ServingManifest](ctx context.Context, store ServingStore, kind ServingKind, ref ObjectRef) (T, []byte, error) {
	var zero T
	if err := ValidateServingRef(ref, store.RootURI(), kind); err != nil {
		return zero, nil, err
	}
	manifest, payload, err := store.ReadServingObject(ctx, kind, ref)
	if err != nil {
		return zero, nil, err
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

// readProposal reads either proposal version with its exact stored bytes.
func readProposal(ctx context.Context, store ServingStore, ref ObjectRef) (ProposalView, []byte, error) {
	manifest, payload, err := readServingFamily(ctx, store, ServingProposal, ref)
	if err != nil {
		return ProposalView{}, nil, err
	}
	typed, ok := manifest.(ProposalManifest)
	if !ok {
		return ProposalView{}, nil, errors.New("registry returned the wrong manifest kind")
	}
	return typed.View(), payload, nil
}

// Prepare validates a frozen proposal without writes; completed retries never require healthy old revisions.
func (c *ServingController) Prepare(ctx context.Context, proposalRef ObjectRef) (PreparationResult, error) {
	logger := c.logger.With("proposal_sha256", proposalRef.SHA256)
	proposal, proposalPayload, err := readProposal(ctx, c.store, proposalRef)
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
		logger.Warn("Serving preparation transition rejected", "generation", snapshot.Generation, "legacy_state_path", snapshot.LegacyPath, "err", err)
		return PreparationResult{}, err
	}
	if transition.Replayed {
		logger.Info("Serving preparation reconciled a previous activation", "activation_id", transition.Activation.ID, "outcome", transition.Outcome)
		return PreparationResult{Proposal: proposalRef, Activation: &transition}, nil
	}
	if len(proposal.WithdrawActivations) > 0 {
		if err := c.validateRollbackSource(ctx, snapshot, proposal); err != nil {
			logger.Warn("Serving preparation rollback source rejected", "source_candidate_sha256", proposal.SourceCandidate.SHA256, "err", err)
			return PreparationResult{}, err
		}
	}
	if err := c.validateProposal(ctx, proposal); err != nil {
		logger.Error("Serving destination validation blocked preparation", "selection_set_sha256", proposal.SelectionSet.SHA256, "err", err)
		return PreparationResult{}, err
	}
	logger.Info("Serving proposal prepared without activation", "selection_set_sha256", proposal.SelectionSet.SHA256, "generation", snapshot.Generation)
	return PreparationResult{Proposal: proposalRef, Prepared: true}, nil
}

// Rollback uses the activation CAS path but only accepts a source previously serving this target.
// Normal rollback retains pins; the proposal explicitly lists emergency withdrawals.
func (c *ServingController) Rollback(ctx context.Context, proposalRef ObjectRef, workflowActor string) (ActivationResult, error) {
	return c.activate(ctx, proposalRef, workflowActor, true)
}

// validateRollbackSource accepts a source candidate that some retained activation, of either
// selection-set version, served for the proposal's target and profile.
func (c *ServingController) validateRollbackSource(ctx context.Context, snapshot ServingStateSnapshot, proposal ProposalView) error {
	for _, activation := range snapshot.State.Activations {
		set, err := readSelectionSetView(ctx, c.store, activation.SelectionSet)
		if err != nil {
			return err
		}
		if set.Target != proposal.Target {
			return errors.New("rollback history belongs to another target")
		}
		profileKey := ""
		if proposal.Scope == ChangeProfile {
			profileKey = proposal.ProfileKey
		}
		if selection, exists := set.selection(profileKey); exists && selection.Release == proposal.SourceCandidate {
			return nil
		}
	}
	return errors.New("rollback requires a known-good source release previously serving the same target and profile")
}

// Activate commits this exact proposal; retries never reactivate a superseded result.
func (c *ServingController) Activate(ctx context.Context, proposalRef ObjectRef, workflowActor string) (ActivationResult, error) {
	return c.activate(ctx, proposalRef, workflowActor, false)
}

func (c *ServingController) activate(ctx context.Context, proposalRef ObjectRef, workflowActor string, rollback bool) (ActivationResult, error) {
	logger := c.logger.With("proposal_sha256", proposalRef.SHA256, "workflow_actor", workflowActor)
	proposal, proposalPayload, err := readProposal(ctx, c.store, proposalRef)
	if err != nil {
		logger.Error("Failed to read immutable serving activation proposal", "err", err)
		return ActivationResult{}, err
	}
	logger = logger.With("target", proposal.Target, "operator", proposal.Actor)
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
		logger.Warn("Serving activation transition rejected", "generation", snapshot.Generation, "legacy_state_path", snapshot.LegacyPath, "err", err)
		return transition, err
	}
	if transition.Replayed {
		logger.Info("Serving activation reconciled a previous outcome", "activation_id", transition.Activation.ID, "outcome", transition.Outcome)
		return transition, nil
	}
	if rollback || len(proposal.WithdrawActivations) > 0 {
		if err := c.validateRollbackSource(ctx, snapshot, proposal); err != nil {
			logger.Warn("Serving rollback source validation rejected", "source_candidate_sha256", proposal.SourceCandidate.SHA256, "err", err)
			return ActivationResult{}, err
		}
	}
	if err := c.validateProposal(ctx, proposal); err != nil {
		logger.Error("Serving proposal validation blocked activation", "err", err)
		return ActivationResult{}, err
	}
	committed, err := c.store.CompareAndSwapServingState(ctx, transition.Snapshot.State, snapshot.WriteGeneration())
	if err != nil {
		logger.Error("Serving activation CAS failed; keep the proposal for outcome reconciliation", "legacy_state_path", snapshot.LegacyPath, "err", err)
		return ActivationResult{}, err
	}
	transition.Snapshot = committed
	logger.Info("Serving activation committed", "activation_id", transition.Activation.ID, "sequence", transition.Activation.Sequence, "generation", committed.Generation, "migrated_state_path", snapshot.LegacyPath)
	return transition, nil
}

// ValidateProposal checks all destination profiles and change scope before activation.
func (c *ServingController) ValidateProposal(ctx context.Context, proposal ProposalManifest) error {
	if err := proposal.Validate(c.store.RootURI()); err != nil {
		return err
	}
	return c.validateProposal(ctx, proposal.View())
}

func (c *ServingController) validateProposal(ctx context.Context, proposal ProposalView) error {
	for _, evidence := range proposal.Evidence {
		if err := c.store.VerifyServingArtifact(ctx, evidence); err != nil {
			return fmt.Errorf("verify proposal evidence: %w", err)
		}
	}
	lanes := newLaneReader(c.store)
	set, err := lanes.selectionSet(ctx, proposal.SelectionSet)
	if err != nil {
		return err
	}
	if set.Target != proposal.Target {
		return errors.New("proposal and selection set targets differ")
	}
	prepared, err := c.validateSelection(ctx, proposal.Target, "", set.Default.Selection)
	if err != nil {
		return fmt.Errorf("default selection: %w", err)
	}
	base, baseBinding := prepared.Candidate, prepared.Binding
	for key, lane := range set.Profiles {
		profile, err := c.validateSelection(ctx, proposal.Target, key, lane.Selection)
		if err != nil {
			return fmt.Errorf("profile %q: %w", key, err)
		}
		if profile.Candidate.RouterImageDigest != base.RouterImageDigest || !profile.Candidate.Classifier.Equal(base.Classifier) {
			return errors.New("profile must share its lane's worker image and classifier bundle")
		}
		if profile.Binding.Router != baseBinding.Router || profile.Binding.Classifier != baseBinding.Classifier {
			return errors.New("profile must reuse its lane's prepared worker and classifier revisions")
		}
	}
	source, err := lanes.candidate(ctx, proposal.SourceCandidate)
	if err != nil {
		return err
	}
	if err := c.store.VerifyServingArtifact(ctx, source.Provenance.BuildAttestation); err != nil {
		return fmt.Errorf("verify source build attestation: %w", err)
	}
	if (proposal.Scope == ChangeFull || proposal.Scope == ChangeRollback) && set.Default.Candidate != proposal.SourceCandidate {
		return errors.New("full promotion must reuse the exact selected source composition")
	}
	if proposal.PreviousSelectionSet == nil {
		if proposal.Scope != ChangeFull {
			return errors.New("bootstrap requires a full release proposal")
		}
		return nil
	}
	previous, err := lanes.selectionSet(ctx, *proposal.PreviousSelectionSet)
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
	for key, lane := range previous.Profiles {
		next, exists := set.Profiles[key]
		if !exists {
			return errors.New("registered profile keys cannot be removed")
		}
		if proposal.Scope != ChangeProfile && !sameLaneProfile(lane.Profile, next.Profile) {
			return errors.New("base promotion must retain destination profile revisions")
		}
		if proposal.Scope == ChangeProfile && key != proposal.ProfileKey || proposal.Scope == ChangeRoster {
			same, err := lanes.sameLane(ctx, lane, next)
			if err != nil {
				return err
			}
			if !same {
				return errors.New("proposal changes an out-of-scope customer tuple")
			}
		}
	}
	for key := range set.Profiles {
		if _, exists := previous.Profiles[key]; !exists && (proposal.Scope != ChangeProfile || key != proposal.ProfileKey) {
			return errors.New("profile registration requires an explicit named-profile proposal")
		}
	}
	if proposal.Scope == ChangeProfile {
		lane, exists := set.Profiles[proposal.ProfileKey]
		sameDefault, err := lanes.sameLane(ctx, previous.Default, set.Default)
		if err != nil {
			return err
		}
		if !exists || !sameDefault {
			return errors.New("profile-only promotion must preserve the default and name a registered profile")
		}
		profileCandidate, err := lanes.candidate(ctx, lane.Candidate)
		if err != nil {
			return err
		}
		if profileCandidate.Policy != source.Policy {
			return errors.New("profile promotion does not use the selected source policy")
		}
		return nil
	}
	oldBase, err := lanes.candidate(ctx, previous.Default.Candidate)
	if err != nil {
		return err
	}
	oldBinding := previous.Default.Binding
	if (proposal.Scope == ChangeRoster || proposal.Scope == ChangeClassifier) && baseBinding.Router != oldBinding.Router {
		return errors.New("component-only proposal must reuse the destination worker revision")
	}
	if (proposal.Scope == ChangeRoster || proposal.Scope == ChangeRouter) && baseBinding.Classifier != oldBinding.Classifier {
		return errors.New("component-only proposal must reuse the destination classifier revision")
	}
	switch proposal.Scope {
	case ChangeRouter:
		if err := c.validateRouterOnlyLanes(ctx, lanes, set, previous, source); err != nil {
			c.logger.Warn("Rejected router-only proposal", "target", proposal.Target, "selection_set_sha256", proposal.SelectionSet.SHA256, "err", err)
			return err
		}
	case ChangeRoster:
		if base.Policy != source.Policy || base.RouterImageDigest != oldBase.RouterImageDigest || !base.Classifier.Equal(oldBase.Classifier) {
			return errors.New("roster-only promotion must retain destination image and classifier")
		}
	case ChangeClassifier:
		if !base.Classifier.Equal(source.Classifier) || base.RouterImageDigest != oldBase.RouterImageDigest || base.Policy != oldBase.Policy {
			return errors.New("classifier-only promotion must retain destination image and policy")
		}
	}
	return nil
}

// validateRouterOnlyLanes admits a Router-only update only when Default and every predecessor
// profile move together: each lane receives a new candidate and binding that differ from their
// predecessors solely by the source Router image, and every successor lane shares one new Router
// revision. Runs after structural selection validation and forward profile preservation.
func (c *ServingController) validateRouterOnlyLanes(ctx context.Context, lanes *laneReader, next, previous resolvedSelectionSet, source CandidateComposition) error {
	if len(next.Profiles) != len(previous.Profiles) {
		return errors.New("router-only promotion must retain the destination profile inventory")
	}
	predecessorRouterConfiguration := previous.Default.Binding.Router.Configuration
	sharedRouter := next.Default.Binding.Router
	if err := c.validateRouterOnlyLane(ctx, lanes, previous.Default, next.Default, source, predecessorRouterConfiguration, sharedRouter); err != nil {
		return fmt.Errorf("default lane: %w", err)
	}
	for key, previousLane := range previous.Profiles {
		nextLane, exists := next.Profiles[key]
		if !exists {
			return errors.New("registered profile keys cannot be removed")
		}
		if err := c.validateRouterOnlyLane(ctx, lanes, previousLane, nextLane, source, predecessorRouterConfiguration, sharedRouter); err != nil {
			return fmt.Errorf("profile %s lane: %w", key, err)
		}
	}
	return nil
}

// validateRouterOnlyLane compares one predecessor/successor lane pair. The successor candidate may
// differ only in Router image digest and provenance; the successor binding may differ only in
// attestation and the shared Router revision.
func (c *ServingController) validateRouterOnlyLane(ctx context.Context, lanes *laneReader, previous, next resolvedLane, source CandidateComposition, predecessorRouterConfiguration ObjectRef, sharedRouter RevisionBinding) error {
	if next.Candidate == previous.Candidate || next.Binding == previous.Binding {
		return errors.New("router-only promotion must publish a new release and binding for every lane")
	}
	previousCandidate, err := lanes.candidate(ctx, previous.Candidate)
	if err != nil {
		return fmt.Errorf("read predecessor release: %w", err)
	}
	nextCandidate, err := lanes.candidate(ctx, next.Candidate)
	if err != nil {
		return fmt.Errorf("read successor release: %w", err)
	}
	if previous.Binding.Router.Configuration != predecessorRouterConfiguration {
		return errors.New("router-only promotion requires an identical predecessor Router configuration across lanes")
	}
	if nextCandidate.RouterImageDigest != source.RouterImageDigest || nextCandidate.Provenance != source.Provenance {
		return errors.New("router-only promotion must carry the source Router image and provenance")
	}
	expectedCandidate := previousCandidate
	expectedCandidate.RouterImageDigest = nextCandidate.RouterImageDigest
	expectedCandidate.Provenance = nextCandidate.Provenance
	if !expectedCandidate.Equal(nextCandidate) {
		return errors.New("router-only promotion must retain destination policy, classifier, and requirements")
	}
	if next.Binding.Router == previous.Binding.Router || next.Binding.Router != sharedRouter {
		return errors.New("router-only promotion must share one new Router revision across every lane")
	}
	expectedBinding := previous.Binding
	expectedBinding.Attestation = next.Binding.Attestation
	expectedBinding.Router = next.Binding.Router
	if expectedBinding != next.Binding {
		return errors.New("router-only promotion must retain destination target, project, region, and classifier revision")
	}
	return nil
}

// resolvedLane is one lane of either selection-set version with its content in hand. Scope checks
// compare candidates, bindings and profile pins by content, so a v2 successor is judged against a
// v1 predecessor whose lane was stored as separate binding and profile objects.
type resolvedLane struct {
	Selection ServingSelection
	Candidate ObjectRef
	Binding   LaneBinding
	Profile   *laneProfile
}

type laneProfile struct {
	Key          string
	Policy       PolicyObject
	Requirements ServingRequirements
}

type resolvedSelectionSet struct {
	Target   ServingTarget
	Default  resolvedLane
	Profiles map[string]resolvedLane
}

func sameLaneProfile(left, right *laneProfile) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	return left == nil || *left == *right
}

// sameLane holds when two lanes serve the same tuple. Candidates are compared by composition so a
// v2 candidate folding a v1 release counts as the same tuple during the layout transition.
func (r *laneReader) sameLane(ctx context.Context, left, right resolvedLane) (bool, error) {
	if left.Binding != right.Binding || !sameLaneProfile(left.Profile, right.Profile) {
		return false, nil
	}
	if left.Candidate == right.Candidate {
		return true, nil
	}
	leftCandidate, err := r.candidate(ctx, left.Candidate)
	if err != nil {
		return false, err
	}
	rightCandidate, err := r.candidate(ctx, right.Candidate)
	if err != nil {
		return false, err
	}
	return leftCandidate.Equal(rightCandidate), nil
}

// laneReader resolves selection sets of either version and memoizes candidate compositions, so
// one proposal validation fetches each candidate once however many lanes share it.
type laneReader struct {
	store      ServingStore
	candidates map[ObjectRef]CandidateComposition
}

func newLaneReader(store ServingStore) *laneReader {
	return &laneReader{store: store, candidates: make(map[ObjectRef]CandidateComposition)}
}

func (r *laneReader) candidate(ctx context.Context, ref ObjectRef) (CandidateComposition, error) {
	if candidate, exists := r.candidates[ref]; exists {
		return candidate, nil
	}
	candidate, err := readCandidate(ctx, r.store, ref)
	if err != nil {
		return CandidateComposition{}, err
	}
	r.candidates[ref] = candidate
	return candidate, nil
}

func (r *laneReader) selectionSet(ctx context.Context, ref ObjectRef) (resolvedSelectionSet, error) {
	manifest, _, err := readServingFamily(ctx, r.store, ServingSelectionSet, ref)
	if err != nil {
		return resolvedSelectionSet{}, err
	}
	switch typed := manifest.(type) {
	case *SelectionSetV2:
		view := typed.View(ref)
		profiles := make(map[string]resolvedLane, len(typed.Profiles))
		for key, lane := range typed.Profiles {
			profiles[key] = resolvedLane{Selection: view.Profiles[key], Candidate: lane.Candidate, Binding: lane.LaneBinding, Profile: &laneProfile{Key: lane.ProfileKey, Policy: *lane.ProfilePolicy, Requirements: *lane.ProfileRequirements}}
		}
		return resolvedSelectionSet{Target: typed.Target, Default: resolvedLane{Selection: view.Default, Candidate: typed.Default.Candidate, Binding: typed.Default.LaneBinding}, Profiles: profiles}, nil
	case *SelectionSet:
		defaultLane, err := r.v1Lane(ctx, typed.Default)
		if err != nil {
			return resolvedSelectionSet{}, err
		}
		profiles := make(map[string]resolvedLane, len(typed.Profiles))
		for key, selection := range typed.Profiles {
			lane, err := r.v1Lane(ctx, selection)
			if err != nil {
				return resolvedSelectionSet{}, fmt.Errorf("profile %q: %w", key, err)
			}
			profiles[key] = lane
		}
		return resolvedSelectionSet{Target: typed.Target, Default: defaultLane, Profiles: profiles}, nil
	default:
		return resolvedSelectionSet{}, errors.New("registry returned the wrong manifest kind")
	}
}

func (r *laneReader) v1Lane(ctx context.Context, selection ServingSelection) (resolvedLane, error) {
	binding, err := readServing[*DeploymentBinding](ctx, r.store, ServingBindings, selection.Binding)
	if err != nil {
		return resolvedLane{}, err
	}
	lane := resolvedLane{Selection: selection, Candidate: selection.Release, Binding: binding.lane()}
	if selection.Profile != nil {
		profile, err := readServing[*RoutingProfile](ctx, r.store, ServingProfiles, *selection.Profile)
		if err != nil {
			return resolvedLane{}, err
		}
		lane.Profile = &laneProfile{Key: profile.ProfileKey, Policy: profile.Policy, Requirements: profile.Requirements}
	}
	return lane, nil
}

// readSelectionSetView reads a selection-set-family reference of either version as the tuples
// admission and rollback compare.
func readSelectionSetView(ctx context.Context, store ServingStore, ref ObjectRef) (SelectionSetView, error) {
	manifest, _, err := readServingFamily(ctx, store, ServingSelectionSet, ref)
	if err != nil {
		return SelectionSetView{}, err
	}
	switch typed := manifest.(type) {
	case *SelectionSetV2:
		return typed.View(ref), nil
	case *SelectionSet:
		return typed.View(), nil
	default:
		return SelectionSetView{}, errors.New("registry returned the wrong manifest kind")
	}
}

func (c *ServingController) validateSelection(ctx context.Context, target ServingTarget, profileKey string, selection ServingSelection) (PreparedSelection, error) {
	prepared, err := ReadPreparedSelection(ctx, c.store, target, profileKey, selection)
	if err != nil {
		return PreparedSelection{}, err
	}
	if err := c.store.VerifyServingArtifact(ctx, prepared.Candidate.Provenance.BuildAttestation); err != nil {
		return PreparedSelection{}, fmt.Errorf("verify destination build attestation: %w", err)
	}
	if err := c.store.VerifyServingArtifact(ctx, prepared.Binding.Attestation); err != nil {
		return PreparedSelection{}, fmt.Errorf("verify physical revision attestation: %w", err)
	}
	if err := c.validator.ValidatePreparedSelection(ctx, prepared); err != nil {
		return PreparedSelection{}, err
	}
	return prepared, nil
}

// readServingFamily reads a reference of a v2 kind, which may resolve to either the v2 object or
// the v1 object it folds. The stored layout must agree with the decoded schema.
func readServingFamily(ctx context.Context, store ServingStore, kind ServingKind, ref ObjectRef) (ServingManifest, []byte, error) {
	if err := ValidateServingRef(ref, store.RootURI(), kind); err != nil {
		return nil, nil, err
	}
	manifest, payload, err := store.ReadServingObject(ctx, kind, ref)
	if err != nil {
		return nil, nil, err
	}
	if isServingV2Manifest(manifest) != isServingArtifactURI(ref.URI, store.RootURI()) {
		return nil, nil, errors.New("serving object schema does not belong to its storage layout")
	}
	if err := manifest.Validate(store.RootURI()); err != nil {
		return nil, nil, err
	}
	return manifest, payload, nil
}

// readCandidate resolves a candidate-family reference into its effective composition. A v1
// release is completed from its classifier bundle; a v2 candidate already carries it.
func readCandidate(ctx context.Context, store ServingStore, ref ObjectRef) (CandidateComposition, error) {
	manifest, _, err := readServingFamily(ctx, store, ServingCandidate, ref)
	if err != nil {
		return CandidateComposition{}, err
	}
	switch typed := manifest.(type) {
	case *CandidateV2:
		return typed.CandidateComposition, nil
	case *ServingRelease:
		bundle, err := readServing[*ClassifierBundle](ctx, store, ServingClassifiers, typed.Classifier)
		if err != nil {
			return CandidateComposition{}, err
		}
		if typed.Requirements.ClassifierWireSchema != bundle.Identity.WireSchema || typed.Requirements.TaxonomySHA256 != bundle.Identity.TaxonomySHA256 {
			return CandidateComposition{}, errors.New("release requirements do not match classifier bundle")
		}
		return typed.composition(bundle.component()), nil
	default:
		return CandidateComposition{}, errors.New("registry returned the wrong manifest kind")
	}
}

// readLane resolves the lane a normalized v2 selection names inside its selection set.
func readLane(ctx context.Context, store ServingStore, target ServingTarget, profileKey string, selection ServingSelection) (ServingLane, error) {
	manifest, _, err := readServingFamily(ctx, store, ServingSelectionSet, selection.Binding)
	if err != nil {
		return ServingLane{}, err
	}
	set, ok := manifest.(*SelectionSetV2)
	if !ok {
		return ServingLane{}, errors.New("lane selection must name a v2 selection set")
	}
	if set.Target != target {
		return ServingLane{}, errors.New("selection set belongs to another target")
	}
	lane, exists := set.lane(profileKey)
	if !exists || lane.Candidate != selection.Release {
		return ServingLane{}, errors.New("selection set has no lane for the selected candidate and profile")
	}
	return lane, nil
}

// ReadPreparedSelection reads and cross-validates one exact tuple without consulting mutable heads.
// v1 selections traverse release, binding, classifier and profile objects; v2 selections read the
// lane embedded in their selection set. Both normalize to the same PreparedSelection.
func ReadPreparedSelection(ctx context.Context, store ServingStore, target ServingTarget, profileKey string, selection ServingSelection) (PreparedSelection, error) {
	if err := selection.validate(store.RootURI(), profileKey != ""); err != nil {
		return PreparedSelection{}, err
	}
	var candidate CandidateComposition
	var binding LaneBinding
	if selection.isLane(store.RootURI()) {
		lane, err := readLane(ctx, store, target, profileKey, selection)
		if err != nil {
			return PreparedSelection{}, err
		}
		if candidate, err = readCandidate(ctx, store, lane.Candidate); err != nil {
			return PreparedSelection{}, err
		}
		if profileKey != "" && (*lane.ProfilePolicy != candidate.Policy || *lane.ProfileRequirements != candidate.Requirements) {
			return PreparedSelection{}, errors.New("effective tuple differs from the assigned profile's immutable policy")
		}
		binding = lane.LaneBinding
	} else {
		release, err := readServing[*ServingRelease](ctx, store, ServingReleases, selection.Release)
		if err != nil {
			return PreparedSelection{}, err
		}
		deployment, err := readServing[*DeploymentBinding](ctx, store, ServingBindings, selection.Binding)
		if err != nil {
			return PreparedSelection{}, err
		}
		bundle, err := readServing[*ClassifierBundle](ctx, store, ServingClassifiers, release.Classifier)
		if err != nil {
			return PreparedSelection{}, err
		}
		if deployment.Target != target || deployment.Release != selection.Release || deployment.ClassifierBundleSHA256 != release.Classifier.SHA256 {
			return PreparedSelection{}, errors.New("deployment binding differs from release or classifier identity/configuration")
		}
		if release.Requirements.ClassifierWireSchema != bundle.Identity.WireSchema || release.Requirements.TaxonomySHA256 != bundle.Identity.TaxonomySHA256 {
			return PreparedSelection{}, errors.New("release requirements do not match classifier bundle")
		}
		if profileKey != "" {
			profile, err := readServing[*RoutingProfile](ctx, store, ServingProfiles, *selection.Profile)
			if err != nil {
				return PreparedSelection{}, err
			}
			if profile.ProfileKey != profileKey || profile.Policy != release.Policy || profile.Requirements != release.Requirements {
				return PreparedSelection{}, errors.New("effective tuple differs from the assigned profile's immutable policy")
			}
		}
		candidate = release.composition(bundle.component())
		binding = deployment.lane()
	}
	if binding.Router.ImageDigest != candidate.RouterImageDigest || binding.Classifier.ImageDigest != candidate.Classifier.Identity.ImageDigest || binding.Classifier.Configuration != candidate.Classifier.Configuration {
		return PreparedSelection{}, errors.New("deployment binding differs from release or classifier identity/configuration")
	}
	policy, err := store.ReadServingPolicy(ctx, ObjectRef{URI: candidate.Policy.URI, SHA256: candidate.Policy.SHA256, Generation: candidate.Policy.Generation})
	if err != nil {
		return PreparedSelection{}, err
	}
	if policy.SchemaVersion != candidate.Requirements.PolicySchema || !slices.Equal(policy.ClassOrder, candidate.Classifier.Identity.ClassOrder) {
		return PreparedSelection{}, errors.New("compiled policy does not match classifier schema/taxonomy")
	}
	return PreparedSelection{Selection: selection, ProfileKey: profileKey, Target: target, Candidate: candidate, Binding: binding, Policy: policy}, nil
}
