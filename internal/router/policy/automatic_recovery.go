package policy

import (
	"context"
	"errors"
	"slices"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
)

// Automatic exclusions fail open against the serving roster, not merely the
// catalog: off-roster candidates must not prevent the last serving arm's rescue.
// Reuse the verified classification and selector so roster pins and escalation
// constraints still govern recovery; no provider or funding gate is relaxed.
func (r *SidecarRouter) selectArmWithAutomaticReadmission(ctx context.Context, req router.Request, input SelectionInput, resolved ResolvedCandidates, constrained bool) (SelectionPick, ResolvedCandidates, error) {
	pick, err := r.selectEscalationArm(ctx, req, input, resolved, constrained)
	if !errors.Is(err, ErrNoEligibleArm) || !slices.ContainsFunc(resolved.Diagnostics, func(d Diagnostic) bool {
		return d.Reason == ExclusionAutomaticDisabled
	}) {
		return pick, resolved, err
	}
	req.AutomaticExcludedModels = cloneModelSet(req.AutomaticExcludedModels)
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Reason == ExclusionAutomaticDisabled {
			delete(req.AutomaticExcludedModels, diagnostic.CatalogID)
		}
	}
	readmitted := r.resolver.Resolve(req)
	input.CandidateRosterIDs = candidateRosterIDs(readmitted)
	recovered, recoveryErr := r.selectEscalationArm(ctx, req, input, readmitted, constrained)
	if recoveryErr != nil {
		return recovered, resolved, recoveryErr
	}
	observability.FromContext(ctx).Warn("Serving roster exhausted; readmitted automatically excluded models",
		"strategy", input.Strategy, "execution_mode", input.ExecutionMode,
		"route_id", input.RouteID, "selected_group", recovered.Group, "selected_arm", recovered.Arm)
	return recovered, readmitted, nil
}

func cloneModelSet(models map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(models))
	for model := range models {
		cloned[model] = struct{}{}
	}
	return cloned
}
