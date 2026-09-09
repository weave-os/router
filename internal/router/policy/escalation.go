package policy

import (
	"context"
	"errors"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
)

func (r *SidecarRouter) selectEscalationArm(ctx context.Context, req router.Request, input SelectionInput, resolved ResolvedCandidates, constrained bool) (SelectionPick, error) {
	pick, err := r.armSelector(ctx, input)
	if !constrained || (err != nil && !errors.Is(err, ErrNoEligibleArm)) {
		return pick, err
	}
	if _, hasOverride := req.ClusterArmOverrides[input.ClassifierGroup]; !hasOverride {
		return pick, err
	}
	// Key-configured arms may extend the artifact roster. Preserve that explicit
	// order, but only inside the successfully constrained class.
	override, overrideErr := ApplyClusterArmOverridesRequireMatch(req.ClusterArmOverrides, input.RankedFallback, resolved, pick.Arm, input.ClassifierGroup)
	if overrideErr != nil {
		return SelectionPick{}, ErrNoEligibleArm
	}
	pick.Group = override.Group
	pick.Arm = override.RosterID
	return pick, nil
}

func constrainEscalation(req router.Request, input SelectionInput, resolved ResolvedCandidates) (SelectionInput, *escalation.Decision, bool) {
	if req.Escalation == nil || req.ForceCluster != "" || req.ForceModel != "" {
		return input, nil, false
	}
	base := escalation.Group(input.ClassifierGroup)
	if escalation.Rank(base) < 0 {
		return input, nil, false
	}
	effective := escalation.Higher(base, req.Escalation.Floor)
	decision := &escalation.Decision{Baseline: base, Effective: effective, Outcome: escalation.OutcomeBelowThreshold}
	hasFloor := escalation.Rank(req.Escalation.Floor) >= 0
	if hasFloor {
		decision.Outcome = escalation.OutcomeFloor
	}
	if req.Escalation.Escalate {
		if effective == escalation.Maximum {
			decision.Outcome = escalation.OutcomeMaximum
		} else {
			effective = escalation.Next(effective)
			decision.Effective = effective
			decision.Outcome = escalation.OutcomePromoted
		}
	}
	if !hasFloor && decision.Outcome != escalation.OutcomePromoted {
		return input, decision, false
	}
	if constrained, available := escalationGroupInput(req, input, resolved, effective); available {
		return constrained, decision, true
	}
	return fallbackEscalation(req, input, resolved, decision)
}

// A failed increment can retain an earlier floor; exhausting that floor returns
// to ordinary routing without claiming that the constraint was applied.
func fallbackEscalation(req router.Request, input SelectionInput, resolved ResolvedCandidates, decision *escalation.Decision) (SelectionInput, *escalation.Decision, bool) {
	failedGroup := decision.Effective
	decision.Constrained = false
	decision.Outcome = escalation.OutcomeNoTarget
	if escalation.Rank(req.Escalation.Floor) >= 0 {
		floorGroup := escalation.Higher(decision.Baseline, req.Escalation.Floor)
		if floorGroup != failedGroup {
			if constrained, available := escalationGroupInput(req, input, resolved, floorGroup); available {
				decision.Effective = floorGroup
				return constrained, decision, true
			}
		}
	}
	decision.Effective = decision.Baseline
	return input, decision, false
}

func escalationGroupInput(req router.Request, input SelectionInput, resolved ResolvedCandidates, effective escalation.Group) (SelectionInput, bool) {
	index := indexCandidates(resolved)
	for _, group := range input.RankedFallback {
		if group.Group != string(effective) {
			continue
		}
		allowed, hasOverride := req.ClusterArmOverrides[group.Group]
		group.EligibleArms = effectiveArms(group, allowed, hasOverride, index.catalogToRoster, index.eligibleRosterIDs)
		if len(group.EligibleArms) == 0 {
			break
		}
		input.ClassifierGroup = group.Group
		input.RankedFallback = []PreviewGroup{group}
		return input, true
	}
	return input, false
}
