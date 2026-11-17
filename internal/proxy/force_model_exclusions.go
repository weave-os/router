package proxy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"weave-os/router/internal/router/catalog"
)

var ErrForcedModelExcluded = errors.New("forced model is excluded")

type ForcedModelExcludedError struct {
	Model  string
	Reason string
}

func (e *ForcedModelExcludedError) Error() string { return e.Reason }

func (e *ForcedModelExcludedError) Unwrap() []error {
	if e.Reason == forcedModelUnfundedReason {
		return []error{ErrForcedModelExcluded, ErrWeaveCreditsDisabled}
	}
	return []error{ErrForcedModelExcluded}
}

func (s *Service) forcedModelBinding(ctx context.Context, model, provider string) (binding, reason string) {
	gateways := s.gatewayProvidersForRequest(ctx)
	if len(gateways) == 0 && !modelPermittedByAllowlist(ctx, model) {
		return "", fmt.Sprintf("%s is not on this organization's allowed-model list", model)
	}
	if _, drop := s.forcePolicyExcludedModels(ctx)[model]; drop {
		return "", fmt.Sprintf("%s is excluded on this installation", model)
	}
	if len(gateways) > 0 {
		return gatewayForcedBinding(model, gateways, s.customBindingsForRequest(ctx))
	}
	excluded := s.credentials.PolicyExcludedProviders(installationExcludedProvidersFromContext(ctx))
	bindings := s.servableBindings(model, provider)
	if funded, denied := weaveCreditsFundedProviders(ctx); denied {
		bindings = slices.DeleteFunc(slices.Clone(bindings), func(b string) bool {
			_, ok := funded[b]
			return !ok
		})
		if len(bindings) == 0 {
			return "", forcedModelUnfundedReason
		}
	}
	if len(excluded) == 0 {
		for _, binding := range bindings {
			if binding == provider {
				return provider, ""
			}
		}
		return bindings[0], ""
	}
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

func (s *Service) forcePolicyExcludedModels(ctx context.Context) map[string]struct{} {
	if len(s.gatewayProvidersForRequest(ctx)) > 0 {
		return s.excludedModelsFor(ctx, nil)
	}
	return s.policyExcludedModels(ctx)
}

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

func (s *Service) servableBindings(model, provider string) []string {
	m, ok := catalog.ByID(model)
	if !ok || len(m.Providers) == 0 {
		return []string{provider}
	}
	out := make([]string, 0, len(m.Providers))
	for _, b := range catalog.RoutableBindings(m) {
		if s.credentials.KeyedProviders != nil {
			if _, keyed := s.credentials.KeyedProviders[b.Provider]; !keyed {
				continue
			}
		}
		out = append(out, b.Provider)
	}
	if len(out) == 0 {
		return []string{provider}
	}
	return out
}
