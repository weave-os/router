package proxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"workweave/router/internal/router/catalog"
)

// ErrForcedModelExcluded is returned when a caller forces a model the
// installation's exclusion policy forbids. Exclusions win over the force, and
// they win loudly: the alternative is a caller who believes they are on the
// model they asked for while every turn is served by something else.
var ErrForcedModelExcluded = errors.New("forced model is excluded")

// ForcedModelExcludedError carries the caller-facing reason a forced model was
// refused, so the HTTP boundary can name the offending model instead of
// emitting a generic policy error.
type ForcedModelExcludedError struct {
	Model  string
	Reason string
}

// Error implements error.
func (e *ForcedModelExcludedError) Error() string { return e.Reason }

// Unwrap ties the typed error to ErrForcedModelExcluded for errors.Is.
func (e *ForcedModelExcludedError) Unwrap() error { return ErrForcedModelExcluded }

// forcedModelExclusionReason explains, in the caller's terms, why exclusion
// policy forbids serving model — "" when it is servable. provider is the
// binding the force resolved to, used for models the catalog doesn't carry
// (passthrough IDs have no binding list to check).
func (s *Service) forcedModelExclusionReason(ctx context.Context, model, provider string) string {
	for _, m := range installationExcludedModelsFromContext(ctx) {
		if m == model {
			return fmt.Sprintf("%s is excluded on this installation", model)
		}
	}
	excluded := s.policyExcludedProviders(ctx)
	if len(excluded) == 0 {
		return ""
	}
	bindings := s.servableBindings(model, provider)
	for _, b := range bindings {
		if _, drop := excluded[b]; !drop {
			return ""
		}
	}
	if len(bindings) == 1 {
		return fmt.Sprintf(
			"%s is only served by %s, which is excluded on this installation",
			model, bindings[0],
		)
	}
	return fmt.Sprintf(
		"every provider serving %s (%s) is excluded on this installation",
		model, strings.Join(bindings, ", "),
	)
}

// servableBindings returns the providers that could actually serve model on
// this deploy: its catalog bindings narrowed to the ones holding keys, or the
// single resolved provider for IDs the catalog doesn't carry. Narrowing
// matters — counting a binding to a provider this deploy never wired would let
// a force through that dispatch then has nowhere to send.
func (s *Service) servableBindings(model, provider string) []string {
	m, ok := catalog.ByID(model)
	if !ok || len(m.Providers) == 0 {
		return []string{provider}
	}
	out := make([]string, 0, len(m.Providers))
	for _, b := range m.Providers {
		if s.deploymentKeyedProviders != nil {
			if _, keyed := s.deploymentKeyedProviders[b.Provider]; !keyed {
				continue
			}
		}
		out = append(out, b.Provider)
	}
	if len(out) == 0 {
		// No keyed binding at all: exclusions aren't the reason this can't be
		// served, so leave it to the paths that own "nothing to dispatch to".
		return []string{provider}
	}
	sort.Strings(out)
	return out
}
