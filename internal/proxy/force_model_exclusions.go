package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"workweave/router/internal/router/catalog"
)

// ErrForcedModelExcluded is returned when a caller forces a model the
// installation's exclusion policy forbids — exclusions win loudly so the
// caller isn't silently served a different model every turn.
var ErrForcedModelExcluded = errors.New("forced model is excluded")

// ForcedModelExcludedError carries the caller-facing reason a forced model
// was refused, so ClassifyDispatchError can name the model in the response.
type ForcedModelExcludedError struct {
	Model  string
	Reason string
}

// Error implements error.
func (e *ForcedModelExcludedError) Error() string { return e.Reason }

// Unwrap ties the typed error to ErrForcedModelExcluded for errors.Is.
func (e *ForcedModelExcludedError) Unwrap() error { return ErrForcedModelExcluded }

// forcedModelBinding resolves the provider a forced model should pin to under
// exclusion policy. reason is "" when the model is servable; otherwise it
// explains the refusal in the caller's terms. provider is the binding the
// force resolved to, and is returned as-is for passthrough IDs the catalog
// doesn't carry.
//
// The returned binding is not always the one passed in: forcing resolves to a
// model's primary provider, so a model whose primary is excluded but whose
// fallback isn't must pin the fallback. Pinning the excluded primary would
// answer "force-model applied" and then lose the pin to the eligibility check
// in runTurnLoop on the very next turn.
func (s *Service) forcedModelBinding(ctx context.Context, model, provider string) (binding, reason string) {
	if _, drop := s.excludedModelsForRequest(ctx)[model]; drop {
		return "", fmt.Sprintf("%s is excluded on this installation", model)
	}
	excluded := s.policyExcludedProviders(ctx)
	if len(excluded) == 0 {
		return provider, ""
	}
	bindings := s.servableBindings(model, provider)
	permitted := make([]string, 0, len(bindings))
	for _, b := range bindings {
		if _, drop := excluded[b]; !drop {
			permitted = append(permitted, b)
		}
	}
	switch {
	case len(permitted) == 0 && len(bindings) == 1:
		return "", fmt.Sprintf(
			"%s is only served by %s, which is excluded on this installation",
			model, bindings[0],
		)
	case len(permitted) == 0:
		return "", fmt.Sprintf(
			"every provider serving %s (%s) is excluded on this installation",
			model, strings.Join(bindings, ", "),
		)
	}
	for _, b := range permitted {
		if b == provider {
			return provider, ""
		}
	}
	return permitted[0], ""
}

// servableBindings returns the catalog bindings for model that hold a
// deployment key, in catalog preference order, or [provider] for passthrough
// IDs. Unkeyed bindings are excluded — they can't be dispatched to, so
// shouldn't count as an escape hatch.
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
	return out
}
