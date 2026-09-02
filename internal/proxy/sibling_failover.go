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
// all failed. Walks CandidateModels (the policy's scored pool for this turn,
// already filtered for capability/context), plus PairedModel last for replayed
// pins. Candidates on the failed provider rank last; context fit uses the same
// dual-estimator as the pre-route overflow filter to reject under-sized peers.
func (s *Service) siblingFailoverDecision(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) (router.Decision, bool) {
	md := failed.Metadata
	if md == nil {
		return router.Decision{}, false
	}
	if gw := s.gatewayProvidersForRequest(ctx); len(gw) > 0 {
		return s.gatewaySiblingDecision(ctx, failed, gw, est, sigSavings, outputReserve)
	}
	if s.deploymentKeyedProviders == nil {
		return router.Decision{}, false
	}
	available := s.keyedProvidersExcluding(s.excludedProvidersForRequest(ctx))
	excludedModels := s.excludedModelsForRequest(ctx)
	// The rescue picks a stand-in on the router's own initiative, so a disabled
	// model must not be resurrected here after the pool already excluded it.
	automaticExcluded := s.globalAutomaticExcludedModels(ctx)

	var sameProvider []router.Decision
	for _, id := range siblingCandidateOrder(md) {
		if id == "" || id == failed.Model {
			continue
		}
		if _, drop := excludedModels[id]; drop {
			continue
		}
		if _, disabled := automaticExcluded[id]; disabled {
			continue
		}
		provider, ok := siblingProvider(id, md.CandidateProviders, available)
		if !ok {
			continue
		}
		if !siblingFitsContext(id, provider, est, sigSavings, outputReserve) {
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

// gatewaySiblingDecision rescues a BYOK-gateway turn onto a sibling reachable
// through a gateway key the request already holds. BYOK disables cross-provider
// failover (foreign provider would 401); gateway siblings re-use the same
// credentials so the restriction doesn't apply. A candidate behind a different
// gateway binding ranks first over one on the same gateway.
func (s *Service) gatewaySiblingDecision(ctx context.Context, failed router.Decision, gw map[string]struct{}, est, sigSavings, outputReserve int) (router.Decision, bool) {
	custom := s.customBindingsForRequest(ctx)
	excludedModels := s.excludedModelsForRequest(ctx)
	automaticExcluded := s.globalAutomaticExcludedModels(ctx)

	var sameProvider []router.Decision
	for _, id := range siblingCandidateOrder(failed.Metadata) {
		if id == "" || id == failed.Model {
			continue
		}
		if _, drop := excludedModels[id]; drop {
			continue
		}
		if _, disabled := automaticExcluded[id]; disabled {
			continue
		}
		provider, ok := gatewayProviderFor(id, custom, gw)
		if !ok {
			continue
		}
		if !siblingFitsContext(id, provider, est, sigSavings, outputReserve) {
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

// gatewaySiblingAllowed reports whether sibling rescue is permitted despite
// BYOK disabling generic failover: the sibling must be served by a held gateway key.
func (s *Service) gatewaySiblingAllowed(ctx context.Context, sibling router.Decision) bool {
	_, held := s.gatewayProvidersForRequest(ctx)[sibling.Provider]
	return held
}

// gatewayProviderFor resolves the gateway binding serving a candidate: the
// first alias-declared provider whose key the request holds.
func gatewayProviderFor(model string, custom map[string][]string, gw map[string]struct{}) (string, bool) {
	for _, p := range custom[model] {
		if _, held := gw[p]; held {
			return p, true
		}
	}
	return "", false
}

// siblingFitsContext mirrors excludeContextOverflowModels for one candidate.
func siblingFitsContext(model, provider string, est, sigSavings, outputReserve int) bool {
	if est <= 0 {
		return true
	}
	needed := est + outputReserve
	if sigSavings > 0 && modelStripsAnthropicSignatures(model) {
		needed -= sigSavings
	}
	return needed <= contextWindowForRequest(model, provider)
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
