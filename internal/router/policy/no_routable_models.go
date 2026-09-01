package policy

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ErrNoRoutableModels signals that resolution came back empty due to the
// installation's own configuration, not a router fault.
var ErrNoRoutableModels = errors.New("policy: configuration leaves no routable model")

// ErrGatewayServesNoDeployedModel is ErrNoRoutableModels narrowed to the
// gateway-exclusive case: no deployed model is aliased by any gateway key.
var ErrGatewayServesNoDeployedModel = fmt.Errorf(
	"policy: no deployed model is aliased by a gateway key: %w", ErrNoRoutableModels)

// emptyCandidateError names why resolution produced no candidate, so the
// caller reports the configuration that has to change rather than a generic
// routing failure.
func emptyCandidateError(diagnostics []Diagnostic) error {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason != ExclusionGatewayNotServed {
			return ErrNoRoutableModels
		}
	}
	if len(diagnostics) == 0 {
		return ErrNoRoutableModels
	}
	return ErrGatewayServesNoDeployedModel
}

// candidateLogFields flattens a resolution into slog fields so a failed
// decision names which models were offered and why the rest were dropped.
func candidateLogFields(resolved ResolvedCandidates) []any {
	offered := make([]string, 0, len(resolved.Candidates))
	for _, c := range resolved.Candidates {
		offered = append(offered, c.CatalogID)
	}
	byReason := make(map[ExclusionReason][]string)
	reasons := make([]ExclusionReason, 0)
	for _, d := range resolved.Diagnostics {
		if _, seen := byReason[d.Reason]; !seen {
			reasons = append(reasons, d.Reason)
		}
		byReason[d.Reason] = append(byReason[d.Reason], d.CatalogID)
	}
	slices.Sort(reasons)
	fields := []any{
		"candidate_count", len(offered),
		"candidates", strings.Join(offered, ","),
	}
	for _, reason := range reasons {
		models := byReason[reason]
		sort.Strings(models)
		fields = append(fields, "excluded_"+string(reason), strings.Join(models, ","))
	}
	return fields
}
