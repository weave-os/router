package policyregistry

import (
	"errors"
	"fmt"
)

const ServingSelectionSetV3 ServingSchema = "router_serving_selection_set_v3"

type CodeBinding struct {
	Candidate ObjectRef `json:"candidate"`
	LaneBinding
}

type SelectionSetV3 struct {
	SchemaVersion ServingSchema           `json:"schema_version"`
	Target        ServingTarget           `json:"target"`
	Code          CodeBinding             `json:"code"`
	DefaultPolicy PolicyObject            `json:"default_policy"`
	Profiles      map[string]PolicyObject `json:"profiles"`
}

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
	if err := s.Code.validate(); err != nil {
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

func (s SelectionSetV3) View(ref ObjectRef) SelectionSetView {
	profiles := make(map[string]ServingSelection, len(s.Profiles))
	for key := range s.Profiles {
		profileRef := ref
		profiles[key] = ServingSelection{Release: s.Code.Candidate, Binding: ref, Profile: &profileRef}
	}
	return SelectionSetView{Target: s.Target, Default: ServingSelection{Release: s.Code.Candidate, Binding: ref}, Profiles: profiles}
}
