package proxy

import (
	"context"

	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
)

// ReasonSiblingFailover marks a turn served by a same-cluster candidate after
// the routed model's own bindings were exhausted.
const ReasonSiblingFailover = "sibling_failover"

// siblingFailoverDecision picks a stand-in for a routed model whose bindings
// all failed. The routing metadata's candidate set is the definition of "same
// cluster": it is the eligible pool the decision was drawn from, already
// filtered for this turn's capability and context requirements and ordered by
// the policy's own preference, so walking it keeps the rescue inside the
// quality band the policy accepted. A replayed session pin carries no
// candidate list, only the runner-up frozen at pin time.
//
// Candidates on the provider that just failed rank last: an overloaded
// provider is the common cause of exhaustion, so a peer served elsewhere is
// likelier to succeed. Reports false when no distinct servable candidate
// exists, including when the deploy has no keyed-provider set at all — that
// legacy mode can't tell which providers are dispatchable.
func (s *Service) siblingFailoverDecision(ctx context.Context, failed router.Decision, estimatedTokens int) (router.Decision, bool) {
	md := failed.Metadata
	if md == nil || s.deploymentKeyedProviders == nil {
		return router.Decision{}, false
	}
	available := s.keyedProvidersExcluding(s.excludedProvidersForRequest(ctx))
	excludedModels := s.excludedModelsForRequest(ctx)

	var sameProvider []router.Decision
	for _, id := range siblingCandidateOrder(md) {
		if id == "" || id == failed.Model {
			continue
		}
		if _, drop := excludedModels[id]; drop {
			continue
		}
		if window := catalog.ContextWindowFor(id); window > 0 && estimatedTokens > window {
			continue
		}
		provider, ok := siblingProvider(id, md.CandidateProviders, available)
		if !ok {
			continue
		}
		candidate := siblingDecisionFor(failed, id, provider)
		if provider == failed.Provider {
			sameProvider = append(sameProvider, candidate)
			continue
		}
		return candidate, true
	}
	if len(sameProvider) > 0 {
		return sameProvider[0], true
	}
	return router.Decision{}, false
}

// siblingCandidateOrder lists rescue candidates in policy-preference order,
// with the pin's runner-up last so replayed pins (no candidate vector) still
// have somewhere to go.
func siblingCandidateOrder(md *router.RoutingMetadata) []string {
	order := make([]string, 0, len(md.CandidateModels)+1)
	order = append(order, md.CandidateModels...)
	return append(order, md.PairedModel)
}

// siblingProvider resolves the provider a candidate dispatches to, preferring
// the one the routing decision resolved for it and falling back to catalog
// binding order.
func siblingProvider(model string, resolved map[string]string, available map[string]struct{}) (string, bool) {
	if p, ok := resolved[model]; ok && p != "" {
		if _, keyed := available[p]; keyed {
			return p, true
		}
	}
	binding, ok := catalog.ResolveBinding(model, available)
	if !ok {
		return "", false
	}
	return binding.Provider, true
}

// siblingDecisionFor rebases a failed decision onto the rescue candidate. The
// arm selection is dropped: it names an upstream of the failed model, and
// carrying it would make binding resolution prioritize a binding the candidate
// doesn't have.
func siblingDecisionFor(failed router.Decision, model, provider string) router.Decision {
	md := *failed.Metadata
	md.SelectedArmID = ""
	md.SelectedUpstreamID = ""
	md.BindingIndex = 0

	out := failed
	out.Model = model
	out.Provider = provider
	out.Reason = ReasonSiblingFailover
	out.Metadata = &md
	return out
}

// keyedProvidersExcluding returns the deployment-keyed providers minus the
// installation's excluded ones, or nil in legacy (unset) mode.
func (s *Service) keyedProvidersExcluding(excluded map[string]struct{}) map[string]struct{} {
	if s.deploymentKeyedProviders == nil {
		return nil
	}
	out := make(map[string]struct{}, len(s.deploymentKeyedProviders))
	for p := range s.deploymentKeyedProviders {
		if _, drop := excluded[p]; drop {
			continue
		}
		out[p] = struct{}{}
	}
	return out
}
