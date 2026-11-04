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

// ErrContextWindowExceeded signals that resolution came back empty because the
// request is too large, so the client receives its native prompt-too-long
// error and compacts rather than being told to change its configuration.
var ErrContextWindowExceeded = errors.New("policy: request context exceeds every eligible model's window")

// ErrImageLimitExceeded signals that no eligible binding can accept the
// request's image count while preserving ErrNoRoutableModels matching.
var ErrImageLimitExceeded = fmt.Errorf(
	"policy: request images exceed every eligible model's per-request image limit: %w", ErrNoRoutableModels)

// emptyCandidateError names why resolution produced no candidate, so the
// caller reports the configuration that has to change rather than a generic
// routing failure. Only a model diagnosed as excluded by size makes this a
// context-window error; later request constraints can still empty an
// overflow-admitted pool for an unrelated reason.
func emptyCandidateError(diagnostics []Diagnostic) error {
	if slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Reason == ExclusionContextWindow }) {
		return ErrContextWindowExceeded
	}
	if slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Reason == ExclusionImageLimit }) {
		return ErrImageLimitExceeded
	}
	if len(diagnostics) > 0 && !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Reason != ExclusionGatewayNotServed }) {
		return ErrGatewayServesNoDeployedModel
	}
	return ErrNoRoutableModels
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
