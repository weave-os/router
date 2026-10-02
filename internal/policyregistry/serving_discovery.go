package policyregistry

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
)

// CurrentPolicy identifies the current activation and its validated policy.
// It never uses a retained session binding or a prepared boot roster.
type CurrentPolicy struct {
	Target             ServingTarget
	ProfileKey         string
	ActivationID       string
	SelectionSetSHA256 string
	CandidateSHA256    string
	PolicySHA256       string
	Roster             *rosterdata.Roster
	Binding            LaneBinding
}

// CurrentPolicySource reads the authoritative target state for internal discovery.
type CurrentPolicySource struct {
	Store ServingStore
}

func (source CurrentPolicySource) ReadCurrentPolicy(ctx context.Context, target ServingTarget, profileKey string) (CurrentPolicy, error) {
	if source.Store == nil {
		return CurrentPolicy{}, ErrNoActivePolicy
	}
	if _, err := target.Environment(); err != nil {
		return CurrentPolicy{}, err
	}
	if profileKey != "" {
		if err := validateProfileKey(profileKey); err != nil {
			return CurrentPolicy{}, err
		}
	}
	state, err := source.Store.ReadServingState(ctx, target)
	if err != nil {
		return CurrentPolicy{}, err
	}
	if err := state.State.Validate(source.Store.RootURI(), target); err != nil {
		return CurrentPolicy{}, err
	}
	activation, exists := state.State.Activations[state.State.CurrentActivationID]
	if !exists {
		return CurrentPolicy{}, ErrNoActivePolicy
	}
	selectionSet, err := readSelectionSetView(ctx, source.Store, activation.SelectionSet)
	if err != nil {
		return CurrentPolicy{}, err
	}
	if selectionSet.Target != target {
		return CurrentPolicy{}, errors.New("active selection set belongs to another target")
	}
	selection, exists := selectionSet.selection(profileKey)
	if !exists {
		return CurrentPolicy{}, ErrNoActivePolicy
	}
	prepared, err := ReadPreparedSelection(ctx, source.Store, target, profileKey, selection)
	if err != nil {
		return CurrentPolicy{}, err
	}
	if diagnostics := armid.ValidateRosterIDs(prepared.Policy.AllArms()); len(diagnostics) != 0 {
		return CurrentPolicy{}, fmt.Errorf("active policy contains %d arms absent from the router catalog", len(diagnostics))
	}
	return CurrentPolicy{
		Target: target, ProfileKey: profileKey, ActivationID: activation.ID,
		SelectionSetSHA256: activation.SelectionSet.SHA256,
		CandidateSHA256:    selection.Release.SHA256,
		PolicySHA256:       prepared.Candidate.Policy.SHA256,
		Roster:             prepared.Policy,
		Binding:            prepared.Binding,
	}, nil
}
