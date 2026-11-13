package policyregistry

import (
	"errors"
	"fmt"
)

// ServingSelectionSetV3 separates a target's code cohort from its roster assignments.
const ServingSelectionSetV3 ServingSchema = "router_serving_selection_set_v3"

// CodeBinding pins the one candidate and physical destination shared by a target's customers.
type CodeBinding struct {
	Candidate ObjectRef `json:"candidate"`
	LaneBinding
}

// SelectionSetV3 assigns immutable rosters without deriving customer-specific candidates.
// Candidate requirements apply to every policy; the candidate's own policy is its bootstrap default.
type SelectionSetV3 struct {
	SchemaVersion ServingSchema           `json:"schema_version"`
	Target        ServingTarget           `json:"target"`
	Code          CodeBinding             `json:"code"`
	DefaultPolicy PolicyObject            `json:"default_policy"`
	Profiles      map[string]PolicyObject `json:"profiles"`
}

// Validate enforces a single code binding and an explicit inventory of configuration-only profiles.
func (s SelectionSetV3) Validate(root string) error {
	if s.SchemaVersion != ServingSelectionSetV3 || s.Profiles == nil {
		return errors.New("selection set requires a supported schema and explicit profile map")
	}
	if _, err := s.Target.Environment(); err != nil {
		return err
	}
	if err := ValidateServingRef(s.Code.Candidate, root, ServingCandidate); err != nil {
		return err
	}
	if err := s.Code.LaneBinding.validate(); err != nil {
		return fmt.Errorf("code binding: %w", err)
	}
	if err := validateServingPolicy(s.DefaultPolicy, root); err != nil {
		return fmt.Errorf("default policy: %w", err)
	}
	for key, policy := range s.Profiles {
		if err := validateProfileKey(key); err != nil {
			return err
		}
		if err := validateServingPolicy(policy, root); err != nil {
			return fmt.Errorf("profile %q: %w", key, err)
		}
	}
	return nil
}

// View retains the existing persisted session and signed assertion representation.
func (s SelectionSetV3) View(ref ObjectRef) SelectionSetView {
	profiles := make(map[string]ServingSelection, len(s.Profiles))
	for key := range s.Profiles {
		profileRef := ref
		profiles[key] = ServingSelection{Release: s.Code.Candidate, Binding: ref, Profile: &profileRef}
	}
	return SelectionSetView{Target: s.Target, Default: ServingSelection{Release: s.Code.Candidate, Binding: ref}, Profiles: profiles}
}
