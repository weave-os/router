package policy

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

var ErrNoRoutableModels = errors.New("policy: configuration leaves no routable model")

var ErrGatewayServesNoDeployedModel = fmt.Errorf(
	"policy: no deployed model is aliased by a gateway key: %w", ErrNoRoutableModels)

var ErrNoProviderCredentials = fmt.Errorf(
	"policy: no enabled provider can dispatch any deployed model: %w", ErrNoRoutableModels)

var ErrContextWindowExceeded = errors.New("policy: request context exceeds every eligible model's window")

var ErrImageLimitExceeded = fmt.Errorf(
	"policy: request images exceed every eligible model's per-request image limit: %w", ErrNoRoutableModels)

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
	if slices.ContainsFunc(diagnostics, isProviderExclusion) &&
		!slices.ContainsFunc(diagnostics, func(d Diagnostic) bool {
			structural := d.Reason == ExclusionUnmappedRoster || d.Reason == ExclusionUnknownCatalogModel
			return !isProviderExclusion(d) && !structural
		}) {
		return ErrNoProviderCredentials
	}
	return ErrNoRoutableModels
}

func isProviderExclusion(d Diagnostic) bool {
	return d.Reason == ExclusionNoProvider || d.Reason == ExclusionProviderPolicy
}

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
