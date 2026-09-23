package policyregistry

import (
	"context"
	"errors"

	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/policy"
)

// PrepareWorker validates the immutable bootstrap closure without contacting
// classifiers. A reused revision must cold-start after its bootstrap classifier
// retires. Private destination validation and admission load the live runtime.
func (c *ServingRuntimeCache) PrepareWorker(ctx context.Context, identity WorkerIdentity, ref ObjectRef) (*Snapshot, error) {
	set, err := readSelectionSetView(ctx, c.store, ref)
	if err != nil {
		return nil, err
	}
	if set.Target != identity.Target {
		return nil, errors.New("prepared selection set belongs to another worker target")
	}
	selections := make(map[string]ServingSelection, len(set.Profiles)+1)
	selections[""] = set.Default
	for key, selection := range set.Profiles {
		selections[key] = selection
	}
	var baseline *Snapshot
	for profileKey, selection := range selections {
		admission := SessionReleaseBinding{Target: identity.Target, ProfileKey: profileKey, Selection: selection}
		prepared, err := ReadPreparedSelection(ctx, c.store, admission.Target, admission.ProfileKey, selection)
		if err != nil {
			return nil, err
		}
		if err := identity.ValidateBinding(prepared.Target, prepared.Binding); err != nil {
			return nil, err
		}
		if diagnostics := armid.ValidateRosterIDs(prepared.Policy.AllArms()); len(diagnostics) != 0 {
			return nil, errors.New("prepared policy contains arms absent from the worker catalog")
		}
		if profileKey == "" {
			baseline = &Snapshot{Candidate: Candidate{Policy: prepared.Policy}}
		}
	}
	return baseline, nil
}

// AdmittedRosterSource exposes the requesting installation's exact roster only.
// A request without admission cannot obtain a default or another customer's roster.
type AdmittedRosterSource struct{}

// DistributionRoster exposes the same immutable customer policy used by selection.
func (AdmittedRosterSource) DistributionRoster(ctx context.Context) (*rosterdata.Roster, error) {
	snapshot := ServingSnapshotFromContext(ctx)
	if snapshot == nil || snapshot.Policy == nil {
		return nil, ErrNoActivePolicy
	}
	return snapshot.Policy, nil
}

// ClusterRoster supplies the admitted policy to discovery and escalation.
func (AdmittedRosterSource) ClusterRoster(ctx context.Context) (policy.RosterSnapshot, error) {
	snapshot := ServingSnapshotFromContext(ctx)
	if snapshot == nil {
		return policy.RosterSnapshot{}, ErrNoActivePolicy
	}
	return snapshotRoster(snapshot), nil
}

// Roster supplies the complete arm union without a mutable cross-profile cache.
func (AdmittedRosterSource) Roster(ctx context.Context) ([]string, error) {
	snapshot := ServingSnapshotFromContext(ctx)
	if snapshot == nil {
		return nil, ErrNoActivePolicy
	}
	return snapshot.Policy.AllArms(), nil
}
