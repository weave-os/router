package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"weave-os/router/internal/router/catalog"
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

// forcedModelBinding resolves the provider a forced model should pin to
// under exclusion policy. reason is "" when servable; provider is returned
// as-is for passthrough IDs the catalog doesn't carry.
//
// The returned binding is not always the input provider: forcing resolves to
// a model's primary, so an excluded primary whose fallback is permitted must
// pin the fallback — otherwise the eligibility check in runTurnLoop drops it.
func (s *Service) forcedModelBinding(ctx context.Context, model, provider string) (binding, reason string) {
	// Checked directly, not via the exclusion set: a forced model may be
	// passthrough-only and so never enters the desugared exclusions.
	if !modelPermittedByAllowlist(ctx, model) {
		return "", fmt.Sprintf("%s is not on this organization's allowed-model list", model)
	}
	if _, drop := s.policyExcludedModels(ctx)[model]; drop {
		return "", fmt.Sprintf("%s is excluded on this installation", model)
	}
	// Gateway-exclusive routing drops every vendor from the eligible set, so
	// resolving to the catalog primary would produce a pin the turn loop then
	// rejects — the force would read as applied and route automatically.
	if gateways := s.gatewayProvidersForRequest(ctx); len(gateways) > 0 {
		return gatewayForcedBinding(model, gateways, s.customBindingsForRequest(ctx))
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

// gatewayForcedBinding pins a forced model to a gateway that aliases it. Only
// a key's model_aliases say what the tenant's endpoint serves, so a model no
// gateway names is refused rather than pinned to an unroutable provider.
func gatewayForcedBinding(
	model string, gateways map[string]struct{}, custom map[string][]string,
) (binding, reason string) {
	for _, provider := range custom[model] {
		if _, ok := gateways[provider]; ok {
			return provider, ""
		}
	}
	return "", fmt.Sprintf(
		"%s isn't aliased by any of this installation's gateway keys", model)
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
