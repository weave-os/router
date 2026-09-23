package policyregistry

import (
	"errors"
	"fmt"
	"maps"
	"time"

	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/google/uuid"
)

const (
	// ServingIdleLifetime is measured from the last admitted request, not stream completion.
	ServingIdleLifetime = 24 * time.Hour
	// ServingRetirementLifetime starts once, when an activation is superseded.
	ServingRetirementLifetime = 7 * 24 * time.Hour
	// ServingDrainGrace exceeds the current maximum 600-second worker request duration.
	ServingDrainGrace = 15 * time.Minute
)

// Activation is an incarnation, not an artifact ID; identical selections can have many incarnations.
type Activation struct {
	ID            string     `json:"id"`
	Sequence      int64      `json:"sequence"`
	SelectionSet  ObjectRef  `json:"selection_set"`
	ActivatedAt   time.Time  `json:"activated_at"`
	SupersededAt  *time.Time `json:"superseded_at,omitempty"`
	WithdrawnAt   *time.Time `json:"withdrawn_at,omitempty"`
	ReplacementID string     `json:"replacement_id,omitempty"`
	Proposal      ObjectRef  `json:"proposal"`
	RequestID     string     `json:"request_id"`
	Actor         string     `json:"actor"`
	WorkflowActor string     `json:"workflow_actor"`
}

// ServingControlState is the sole mutable target authority, updated in one GCS CAS.
type ServingControlState struct {
	SchemaVersion       ServingSchema         `json:"schema_version"`
	Target              ServingTarget         `json:"target"`
	CurrentActivationID string                `json:"current_activation_id"`
	Sequence            int64                 `json:"sequence"`
	Activations         map[string]Activation `json:"activations"`
}

// ServingStateSnapshot binds state bytes to the concurrency token read with them.
type ServingStateSnapshot struct {
	State      ServingControlState `json:"state"`
	Generation int64               `json:"generation"`
}

// ActivationOutcome describes a successful CAS or a previous idempotent success.
type ActivationOutcome string

const (
	ActivationCurrent    ActivationOutcome = "activated"
	ActivationSuperseded ActivationOutcome = "superseded"
)

// ActivationResult preserves success even if an idempotent retry arrives after supersession.
type ActivationResult struct {
	Snapshot   ServingStateSnapshot `json:"snapshot"`
	Activation Activation           `json:"activation"`
	Outcome    ActivationOutcome    `json:"outcome"`
	Replayed   bool                 `json:"replayed"`
}

// Validate rejects unknown or inconsistent lifecycle state rather than admitting an unpinned request.
func (s ServingControlState) Validate(root string, target ServingTarget) error {
	if _, err := target.Environment(); err != nil {
		return err
	}
	if s.SchemaVersion != ServingControlStateV1 || s.Target != target || s.Sequence <= 0 {
		return errors.New("invalid serving control-state schema, target or sequence")
	}
	current, exists := s.Activations[s.CurrentActivationID]
	if !exists || current.Sequence != s.Sequence || current.SupersededAt != nil || current.WithdrawnAt != nil {
		return errors.New("current activation is missing, superseded or withdrawn")
	}
	sequences := make(map[int64]struct{}, len(s.Activations))
	requests := make(map[string]struct{}, len(s.Activations))
	for id, activation := range s.Activations {
		parsedID, err := uuid.Parse(id)
		if err != nil || parsedID == uuid.Nil || id != parsedID.String() || id != activation.ID || activation.Sequence <= 0 || activation.Sequence > s.Sequence || activation.ActivatedAt.IsZero() || activation.Actor == "" || activation.WorkflowActor == "" {
			return errors.New("invalid retained activation identity")
		}
		if _, exists := sequences[activation.Sequence]; exists {
			return errors.New("duplicate activation sequence")
		}
		sequences[activation.Sequence] = struct{}{}
		if _, err := uuid.Parse(activation.RequestID); err != nil || activation.RequestID == uuid.Nil.String() {
			return errors.New("invalid activation request identity")
		}
		if _, exists := requests[activation.RequestID]; exists {
			return errors.New("duplicate activation idempotency identity")
		}
		requests[activation.RequestID] = struct{}{}
		if err := ValidateServingRef(activation.SelectionSet, root, ServingSelectionSets); err != nil {
			return err
		}
		if err := ValidateServingRef(activation.Proposal, root, ServingProposals); err != nil {
			return err
		}
		if id != s.CurrentActivationID && activation.SupersededAt == nil {
			return errors.New("outgoing activation has no supersession deadline")
		}
		if activation.SupersededAt != nil && (activation.SupersededAt.Before(activation.ActivatedAt) || activation.SupersededAt.After(current.ActivatedAt)) {
			return errors.New("invalid activation supersession time")
		}
		if (activation.WithdrawnAt != nil) != (activation.ReplacementID != "") {
			return errors.New("withdrawal must name its replacement activation")
		}
		if activation.WithdrawnAt != nil {
			replacement, exists := s.Activations[activation.ReplacementID]
			if !exists || replacement.Sequence <= activation.Sequence || activation.WithdrawnAt.Before(activation.ActivatedAt) || !activation.WithdrawnAt.Equal(replacement.ActivatedAt) {
				return errors.New("invalid same-target withdrawal replacement")
			}
		}
	}
	return nil
}

// NextServingActivation is pure; persistence must CAS the returned state against snapshot.Generation.
// Callers must validate the proposed selection set and its prepared bindings before invoking it.
// proposalPayload must be the exact stored bytes proposalRef names: the transition derives the
// proposal from those digest-verified bytes so approval binds the immutable object, not this
// binary's canonical re-encoding.
func NextServingActivation(snapshot ServingStateSnapshot, proposalPayload []byte, proposalRef ObjectRef, root, workflowActor string, now time.Time) (ActivationResult, error) {
	manifest, err := DecodeStoredServingManifest(proposalPayload, root, ServingProposals)
	if err != nil {
		return ActivationResult{}, err
	}
	proposal, ok := manifest.(*DeploymentProposal)
	if !ok {
		return ActivationResult{}, errors.New("registry returned the wrong proposal manifest kind")
	}
	if err := ValidateServingRef(proposalRef, root, ServingProposals); err != nil {
		return ActivationResult{}, err
	}
	if Digest(proposalPayload) != proposalRef.SHA256 || workflowActor == "" || now.IsZero() {
		return ActivationResult{}, errors.New("proposal digest, execution identity or activation clock is invalid")
	}
	if snapshot.Generation < 0 {
		return ActivationResult{}, errors.New("negative serving state generation")
	}
	if snapshot.Generation > 0 {
		if err := snapshot.State.Validate(root, proposal.Target); err != nil {
			return ActivationResult{}, err
		}
		for _, previous := range snapshot.State.Activations {
			if previous.RequestID != proposal.RequestID {
				continue
			}
			if previous.Proposal != proposalRef {
				return ActivationResult{}, fmt.Errorf("request ID already belongs to a different proposal: %w", ErrConflict)
			}
			outcome := ActivationCurrent
			if previous.ID != snapshot.State.CurrentActivationID {
				outcome = ActivationSuperseded
			}
			return ActivationResult{Snapshot: snapshot, Activation: previous, Outcome: outcome, Replayed: true}, nil
		}
	} else if snapshot.State.CurrentActivationID != "" || len(snapshot.State.Activations) != 0 || snapshot.State.Sequence != 0 {
		return ActivationResult{}, errors.New("bootstrap requires an absent target state")
	}
	if snapshot.Generation != proposal.ExpectedGeneration {
		return ActivationResult{}, fmt.Errorf("target generation changed; create a new preview: %w", ErrConflict)
	}
	if proposal.CreatedAt.After(now) {
		return ActivationResult{}, errors.New("proposal creation time is after activation")
	}
	state := ServingControlState{SchemaVersion: ServingControlStateV1, Target: proposal.Target, Sequence: snapshot.State.Sequence + 1, Activations: maps.Clone(snapshot.State.Activations)}
	if state.Activations == nil {
		state.Activations = make(map[string]Activation)
	}
	if snapshot.Generation > 0 {
		previous := state.Activations[snapshot.State.CurrentActivationID]
		if proposal.PreviousSelectionSet == nil || *proposal.PreviousSelectionSet != previous.SelectionSet {
			return ActivationResult{}, fmt.Errorf("proposal previous selection differs from target: %w", ErrConflict)
		}
		if now.Before(previous.ActivatedAt) {
			return ActivationResult{}, errors.New("activation clock moved backward")
		}
		previous.SupersededAt = &now
		state.Activations[previous.ID] = previous
	}
	activation := Activation{ID: uuid.NewString(), Sequence: state.Sequence, SelectionSet: proposal.SelectionSet, ActivatedAt: now, Proposal: proposalRef, RequestID: proposal.RequestID, Actor: proposal.Actor, WorkflowActor: workflowActor}
	state.CurrentActivationID = activation.ID
	state.Activations[activation.ID] = activation
	for _, id := range proposal.WithdrawActivations {
		withdrawn, exists := state.Activations[id]
		if !exists || id == activation.ID {
			return ActivationResult{}, errors.New("cannot withdraw an unknown activation or another target's activation")
		}
		// A later rollback must not rewrite historical withdrawal metadata.
		if withdrawn.WithdrawnAt == nil {
			withdrawn.WithdrawnAt = &now
			withdrawn.ReplacementID = activation.ID
			state.Activations[id] = withdrawn
		}
	}
	if err := state.Validate(root, proposal.Target); err != nil {
		return ActivationResult{}, err
	}
	return ActivationResult{Snapshot: ServingStateSnapshot{State: state, Generation: snapshot.Generation}, Activation: activation, Outcome: ActivationCurrent}, nil
}

// SessionReleaseBinding is the release decision persisted under an authenticated conversation scope.
type SessionReleaseBinding struct {
	Target               ServingTarget    `json:"target"`
	ActivationID         string           `json:"activation_id"`
	Selection            ServingSelection `json:"selection"`
	ProfileKey           string           `json:"profile_key,omitempty"`
	ProfileName          string           `json:"profile_name,omitempty"`
	Plan                 entitlement.Plan `json:"plan,omitempty"`
	EntitlementVersion   int64            `json:"entitlement_version,omitempty"`
	EnrollmentGeneration int64            `json:"enrollment_generation"`
	AssignmentGeneration int64            `json:"assignment_generation"`
	BindingGeneration    int64            `json:"binding_generation"`
	CreatedAt            time.Time        `json:"created_at"`
	LastAdmittedAt       time.Time        `json:"last_admitted_at"`
}

// AdmissionProjection is read from authenticated router storage, never caller headers.
type AdmissionProjection struct {
	Target               ServingTarget
	ProfileKey           string
	ProfileName          string
	Plan                 entitlement.Plan
	EntitlementVersion   int64
	EnrollmentGeneration int64
	AssignmentGeneration int64
}

// SelectSessionRelease applies pin lifetimes to an authoritative target read inside session serialization.
// A nil previous binding is request-scoped when no canonical client conversation ID exists.
// sets must hold each activation's selection set keyed by its activation-declared SHA256, as
// returned by digest- and generation-verified store reads.
func SelectSessionRelease(previous *SessionReleaseBinding, projection AdmissionProjection, snapshot ServingStateSnapshot, sets map[string]SelectionSet, root string, now time.Time) (SessionReleaseBinding, error) {
	if snapshot.Generation <= 0 || now.IsZero() || projection.EnrollmentGeneration < 0 || projection.AssignmentGeneration < 0 {
		return SessionReleaseBinding{}, errors.New("admission requires authoritative state, projection and clock")
	}
	if err := snapshot.State.Validate(root, projection.Target); err != nil {
		return SessionReleaseBinding{}, err
	}
	if projection.ProfileKey != "" {
		if err := validateProfileKey(projection.ProfileKey); err != nil {
			return SessionReleaseBinding{}, err
		}
	}
	if (projection.Plan == "") != (projection.EntitlementVersion == 0) {
		return SessionReleaseBinding{}, errors.New("plan and entitlement version must be projected together")
	}
	if projection.Plan == "" && projection.ProfileName != "" {
		return SessionReleaseBinding{}, errors.New("profile name requires a subscriber plan")
	}
	if projection.Plan != "" {
		profile, ok := entitlement.ServingProfileFor(projection.Plan)
		if !ok || projection.EntitlementVersion <= 0 || projection.ProfileKey != profile.Key || projection.ProfileName != profile.Name {
			return SessionReleaseBinding{}, errors.New("subscriber plan profile projection is invalid")
		}
	}
	current := snapshot.State.Activations[snapshot.State.CurrentActivationID]
	if now.Before(current.ActivatedAt) {
		return SessionReleaseBinding{}, errors.New("admission clock precedes target activation")
	}
	generation := int64(1)
	createdAt := now
	if previous != nil {
		if previous.BindingGeneration <= 0 || previous.CreatedAt.IsZero() || previous.LastAdmittedAt.Before(previous.CreatedAt) || previous.LastAdmittedAt.After(now) {
			return SessionReleaseBinding{}, errors.New("invalid persisted session binding")
		}
		generation = previous.BindingGeneration + 1
		createdAt = previous.CreatedAt
		if previous.Target == projection.Target {
			activation, exists := snapshot.State.Activations[previous.ActivationID]
			if !exists {
				return SessionReleaseBinding{}, errors.New("persisted session references an unknown activation")
			}
			selected, err := selectionForActivation(activation, previous.ProfileKey, sets, root, projection.Target)
			if err != nil {
				return SessionReleaseBinding{}, err
			}
			if !sameSelection(selected, previous.Selection) {
				return SessionReleaseBinding{}, errors.New("persisted session selection differs from its activation")
			}
			eligible := previous.ProfileKey == projection.ProfileKey && previous.ProfileName == projection.ProfileName && previous.Plan == projection.Plan && previous.EntitlementVersion == projection.EntitlementVersion && previous.EnrollmentGeneration == projection.EnrollmentGeneration && previous.AssignmentGeneration == projection.AssignmentGeneration && now.Before(previous.LastAdmittedAt.Add(ServingIdleLifetime)) && activation.WithdrawnAt == nil && (activation.SupersededAt == nil || now.Before(activation.SupersededAt.Add(ServingRetirementLifetime)))
			if eligible {
				retained := *previous
				retained.LastAdmittedAt = now
				return retained, nil
			}
		}
	}
	selection, err := selectionForActivation(current, projection.ProfileKey, sets, root, projection.Target)
	if err != nil {
		return SessionReleaseBinding{}, err
	}
	return SessionReleaseBinding{Target: projection.Target, ActivationID: current.ID, Selection: selection, ProfileKey: projection.ProfileKey, ProfileName: projection.ProfileName, Plan: projection.Plan, EntitlementVersion: projection.EntitlementVersion, EnrollmentGeneration: projection.EnrollmentGeneration, AssignmentGeneration: projection.AssignmentGeneration, BindingGeneration: generation, CreatedAt: createdAt, LastAdmittedAt: now}, nil
}

func selectionForActivation(activation Activation, profileKey string, sets map[string]SelectionSet, root string, target ServingTarget) (ServingSelection, error) {
	set, exists := sets[activation.SelectionSet.SHA256]
	if !exists {
		return ServingSelection{}, errors.New("activation selection set unavailable")
	}
	if err := set.Validate(root); err != nil {
		return ServingSelection{}, err
	}
	if set.Target != target {
		return ServingSelection{}, errors.New("activation selection-set identity mismatch")
	}
	if profileKey == "" {
		return set.Default, nil
	}
	selection, exists := set.Profiles[profileKey]
	if !exists {
		return ServingSelection{}, errors.New("assigned profile unavailable; default fallback is forbidden")
	}
	return selection, nil
}

func sameSelection(left, right ServingSelection) bool {
	if left.Release != right.Release || left.Binding != right.Binding || (left.Profile == nil) != (right.Profile == nil) {
		return false
	}
	return left.Profile == nil || *left.Profile == *right.Profile
}
