package proxy

import (
	"context"
	"slices"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// ReasonSiblingFailover marks a turn served by a same-cluster candidate after
// the routed model's own bindings were exhausted.
const ReasonSiblingFailover = "sibling_failover"

// siblingFailoverDecisions lists the stand-ins for a routed model whose bindings
// all failed, in the order the turn tries them: the policy's ranked group
// fallback (RescueModels), then the rest of the scored pool (CandidateModels),
// plus PairedModel last for replayed pins. Candidates on the failed provider
// rank after every cross-provider one; context fit uses the same dual-estimator
// as the pre-route overflow filter to reject under-sized peers.
func (s *Service) siblingFailoverDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision {
	md := failed.Metadata
	if md == nil {
		return nil
	}
	return s.rescueDecisions(ctx, failed, siblingCandidateOrder(md), ReasonSiblingFailover, est, sigSavings, outputReserve)
}

// rescueDecision resolves the first candidate the request is allowed to reach.
func (s *Service) rescueDecision(ctx context.Context, failed router.Decision, candidates []string, reason string, est, sigSavings, outputReserve int) (router.Decision, bool) {
	decisions := s.rescueDecisions(ctx, failed, candidates, reason, est, sigSavings, outputReserve)
	if len(decisions) == 0 {
		return router.Decision{}, false
	}
	return decisions[0], true
}

// rescueDecisions resolves every candidate the request is allowed to reach, in
// try order, under the same availability, exclusion, context-fit and
// BYOK-gateway rules for every in-turn rescue (sibling failover,
// safety-refusal retry).
func (s *Service) rescueDecisions(ctx context.Context, failed router.Decision, candidates []string, reason string, est, sigSavings, outputReserve int) []router.Decision {
	if gw := s.gatewayProvidersForRequest(ctx); len(gw) > 0 {
		return s.gatewayRescueDecisions(ctx, failed, candidates, reason, gw, est, sigSavings, outputReserve)
	}
	if s.deploymentKeyedProviders == nil {
		return nil
	}
	available := s.keyedProvidersExcluding(s.excludedProvidersForRequest(ctx))
	excludedModels := s.excludedModelsForRequest(ctx)
	var candidateProviders map[string]string
	if failed.Metadata != nil {
		candidateProviders = failed.Metadata.CandidateProviders
	}
	// The rescue picks a stand-in on the router's own initiative, so a disabled
	// model must not be resurrected here after the pool already excluded it.
	automaticExcluded := s.rescueExcludedModels(ctx)

	var crossProvider, sameProvider []router.Decision
	for _, id := range candidates {
		if id == "" || id == failed.Model {
			continue
		}
		if _, drop := excludedModels[id]; drop {
			continue
		}
		if _, disabled := automaticExcluded[id]; disabled {
			continue
		}
		provider, ok := siblingProvider(id, candidateProviders, available)
		if !ok {
			continue
		}
		if !siblingFitsContext(id, provider, est, sigSavings, outputReserve) {
			continue
		}
		candidate := rescueDecisionFor(failed, id, provider, reason)
		if provider == failed.Provider {
			sameProvider = append(sameProvider, candidate)
			continue
		}
		crossProvider = append(crossProvider, candidate)
	}
	return append(crossProvider, sameProvider...)
}

// rescueExcludedModels is the soft exclusion set an in-turn rescue must honor:
// the deployment-wide set plus the models this session demoted.
func (s *Service) rescueExcludedModels(ctx context.Context) map[string]struct{} {
	demoted := sessionDemotedModelsFromContext(ctx)
	if len(demoted) == 0 {
		return s.globalAutomaticExcludedModels(ctx)
	}
	session := make(map[string]struct{}, len(demoted))
	for _, model := range demoted {
		session[model] = struct{}{}
	}
	return mergeExcludedModels(session, s.globalAutomaticExcludedModels(ctx))
}

// gatewayRescueDecisions rescues a BYOK-gateway turn onto candidates reachable
// through a gateway key the request already holds. BYOK disables cross-provider
// failover (foreign provider would 401); gateway candidates re-use the same
// credentials so the restriction doesn't apply. Candidates behind a different
// gateway binding rank before those on the same gateway.
func (s *Service) gatewayRescueDecisions(ctx context.Context, failed router.Decision, candidates []string, reason string, gw map[string]struct{}, est, sigSavings, outputReserve int) []router.Decision {
	custom := s.customBindingsForRequest(ctx)
	excludedModels := s.excludedModelsForRequest(ctx)
	automaticExcluded := s.rescueExcludedModels(ctx)

	var crossProvider, sameProvider []router.Decision
	for _, id := range candidates {
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
		candidate := rescueDecisionFor(failed, id, provider, reason)
		if provider == failed.Provider {
			sameProvider = append(sameProvider, candidate)
			continue
		}
		crossProvider = append(crossProvider, candidate)
	}
	return append(crossProvider, sameProvider...)
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

// siblingCandidateOrder lists rescue candidates in policy-preference order:
// the ranked group fallback first, then the rest of the scored pool in catalog
// order, with the pin's runner-up last so replayed pins (no candidate vector)
// still have somewhere to go. Deduplicated so a rescue never retries a model
// that already failed this turn.
func siblingCandidateOrder(md *router.RoutingMetadata) []string {
	merged := slices.Concat(md.RescueModels, md.CandidateModels, []string{md.PairedModel})
	order := make([]string, 0, len(merged))
	seen := make(map[string]struct{}, len(merged))
	for _, id := range merged {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		order = append(order, id)
	}
	return order
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

// rescueDecisionFor rebases a failed decision onto the rescue candidate. The
// arm selection is dropped: it names an upstream of the failed model, and
// carrying it would make binding resolution prioritize a binding the candidate
// doesn't have. Effort goes with it — it was chosen against the failed model's
// menu, and keeping it would persist an identity the candidate never served.
func rescueDecisionFor(failed router.Decision, model, provider, reason string) router.Decision {
	out := failed
	out.Model = model
	out.Provider = provider
	out.Effort = ""
	out.Reason = reason
	if failed.Metadata != nil {
		md := *failed.Metadata
		md.SelectedArmID = ""
		md.SelectedUpstreamID = ""
		md.BindingIndex = 0
		out.Metadata = &md
	}
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
