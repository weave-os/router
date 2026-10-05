package proxy

import (
	"context"
	"slices"
	"sort"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// ReasonSiblingFailover marks a turn served by a same-cluster candidate after
// the routed model's own bindings were exhausted.
const ReasonSiblingFailover = "sibling_failover"

// rescueBasisForTurn gives a held automatic pin this request's scored rescue
// shortlist without changing the primary decision's selected arm or binding.
func rescueBasisForTurn(decision router.Decision, turn turnLoopResult) router.Decision {
	if decision.Metadata == nil && turn.StickyHit && !turn.HardPinned && turn.Fresh.Metadata != nil {
		metadata := *turn.Fresh.Metadata
		heldTier := catalog.TierFor(decision.Model)
		metadata.RescueModels = retainRescueModelsAtOrAboveTier(metadata.RescueModels, heldTier)
		metadata.CandidateModels = retainRescueModelsAtOrAboveTier(metadata.CandidateModels, heldTier)
		if catalog.TierFor(metadata.PairedModel) < heldTier {
			metadata.PairedModel = ""
		}
		decision.Metadata = &metadata
	}
	return decision
}

func retainRescueModelsAtOrAboveTier(modelIDs []string, minimumTier catalog.Tier) []string {
	retained := make([]string, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		if minimumTier == catalog.TierUnknown || catalog.TierFor(modelID) >= minimumTier {
			retained = append(retained, modelID)
		}
	}
	return retained
}

// siblingFailoverDecisions lists the stand-ins for a routed model whose bindings
// all failed. Roster-backed routes use only their ordered eligible groups;
// legacy routes append the scored pool and paired model, then prefer other
// providers. Context fit rejects under-sized peers.
func (s *Service) siblingFailoverDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision {
	md := failed.Metadata
	if md == nil {
		return nil
	}
	if md.ClusterRouterVersion != "" && !md.RosterFailover {
		if order := tierRescueModels(failed.Model, md.CandidateModels, md.CandidateScores); len(order) > 0 {
			ordered := *md
			ordered.RescueModels = order
			ordered.RosterFailover = true
			failed.Metadata = &ordered
			md = &ordered
		}
	}
	return s.rescueDecisions(ctx, failed, siblingCandidateOrder(md), ReasonSiblingFailover, est, sigSavings, outputReserve)
}

func tierRescueModels(selected string, candidates []string, scores map[string]float32) []string {
	tier := catalog.TierFor(selected)
	if tier == catalog.TierUnknown {
		return nil
	}
	var ordered []string
	for level := tier; level <= catalog.TierHigh; level++ {
		var group []string
		for _, model := range candidates {
			if catalog.TierFor(model) == level {
				group = append(group, model)
			}
		}
		sort.SliceStable(group, func(i, j int) bool {
			return scores[group[i]] > scores[group[j]]
		})
		ordered = append(ordered, group...)
	}
	return ordered
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
	providerFor := func(id string) (string, bool) {
		return siblingProvider(id, candidateProviders, available)
	}
	return s.rescueWalkOrReadmitCooling(ctx, failed, candidates, reason, excludedModels, automaticExcluded, est, sigSavings, outputReserve, providerFor)
}

// rescueWalkOrReadmitCooling walks the candidates honouring every exclusion,
// then, when the session has rate-limit cooldowns in force, appends the
// cooling arms soonest-to-recover first as the walk's last resort: the rescue
// loop only reaches them once every eligible candidate has failed pre-commit.
// The scorer drops automatically excluded models from CandidateModels, so a
// cooling arm is looked up by name (catalog binding or gateway alias) when the
// scored list lacks it. Hard exclusions (excludedModels) still hold, and the
// walk never returns failed.Model, so the exhausted case prefers a cooled arm
// to the arm that just failed.
//
// Only when neither walk finds anything does a sibling failover readmit
// session-lifetime demotions: a session whose every arm has been struck out
// still gets one rescue attempt on an arm that failed on an earlier turn
// instead of surfacing this turn's failure. Other rescues (the cyber-refusal
// retry) walk their own ordered fallbacks and never readmit a strike.
func (s *Service) rescueWalkOrReadmitCooling(
	ctx context.Context,
	failed router.Decision,
	candidates []string,
	reason string,
	excludedModels, automaticExcluded map[string]struct{},
	est, sigSavings, outputReserve int,
	providerFor func(id string) (string, bool),
) []router.Decision {
	decisions := walkRescueCandidates(failed, candidates, reason, excludedModels, automaticExcluded, est, sigSavings, outputReserve, providerFor)
	decisions = append(decisions, s.readmitCoolingRescueCandidates(ctx, failed, candidates, reason, excludedModels, est, sigSavings, outputReserve, providerFor)...)
	struck := strikesInRescuePool(failed, candidates, sessionStrikeReadmitModelsFromContext(ctx))
	if len(decisions) > 0 || len(struck) == 0 || reason != ReasonSiblingFailover {
		return decisions
	}
	return s.readmissionWalk(ctx, failed, candidates, struck, reason, excludedModels, est, sigSavings, outputReserve, providerFor)
}

func (s *Service) readmitCoolingRescueCandidates(
	ctx context.Context,
	failed router.Decision,
	candidates []string,
	reason string,
	excludedModels map[string]struct{},
	est, sigSavings, outputReserve int,
	providerFor func(id string) (string, bool),
) []router.Decision {
	cooling := sessionCooldownModelsFromContext(ctx)
	if len(cooling) == 0 {
		return nil
	}
	readmitted := s.readmissionWalk(ctx, failed, candidates, cooldownsByExpiry(cooling), reason, excludedModels, est, sigSavings, outputReserve, providerFor)
	sort.SliceStable(readmitted, func(i, j int) bool {
		return cooling[readmitted[i].Model].Before(cooling[readmitted[j].Model])
	})
	return readmitted
}

// readmissionWalk walks candidates plus the readmitted models, lifting only
// their soft exclusion: every other candidate was already covered by an
// earlier walk, and the deployment-wide automatic exclusion still holds.
func (s *Service) readmissionWalk(
	ctx context.Context,
	failed router.Decision,
	candidates, readmit []string,
	reason string,
	excludedModels map[string]struct{},
	est, sigSavings, outputReserve int,
	providerFor func(id string) (string, bool),
) []router.Decision {
	pool := make([]string, 0, len(candidates)+len(readmit))
	pool = append(pool, candidates...)
	for _, model := range readmit {
		if rosterRescueAdmits(failed, model) && !slices.Contains(pool, model) {
			pool = append(pool, model)
		}
	}
	readmitExcluded := make(map[string]struct{}, len(pool))
	for _, id := range pool {
		if !slices.Contains(readmit, id) {
			readmitExcluded[id] = struct{}{}
		}
	}
	readmitExcluded = mergeExcludedModels(readmitExcluded, s.globalAutomaticExcludedModels(ctx))
	return walkRescueCandidates(failed, pool, reason, excludedModels, readmitExcluded, est, sigSavings, outputReserve, providerFor)
}

// rosterRescueAdmits bounds readmission on a roster-failover turn to the
// pool the policy would have rescued onto before soft exclusions.
func rosterRescueAdmits(failed router.Decision, model string) bool {
	md := failed.Metadata
	if md == nil || !md.RosterFailover {
		return true
	}
	if md.ClusterRouterVersion != "" {
		return slices.Contains(md.ScorerRescuePool, model) &&
			catalog.TierFor(model) >= catalog.TierFor(failed.Model) &&
			catalog.TierFor(model) <= catalog.TierHigh
	}
	return slices.Contains(md.RescueModels, model) || slices.Contains(md.SidecarRescuePool, model)
}

// strikesInRescuePool keeps the struck models this turn could have rescued
// onto before soft exclusions, so a last-resort readmission never leaves the
// turn's scored pool. Roster failover is bounded later by rosterRescueAdmits.
func strikesInRescuePool(failed router.Decision, candidates, struck []string) []string {
	md := failed.Metadata
	if md == nil || md.RosterFailover {
		return struck
	}
	var out []string
	for _, model := range struck {
		if slices.Contains(candidates, model) || slices.Contains(md.ScorerRescuePool, model) || slices.Contains(md.SidecarRescuePool, model) {
			out = append(out, model)
		}
	}
	return out
}

// noteRescueReadmission records, as the rescue loop dispatches a candidate,
// that the candidate is a soft-excluded arm readmitted because no eligible
// candidate was left: the session's rescue pool was exhausted.
func (s *Service) noteRescueReadmission(ctx context.Context, failed, rescuer router.Decision) {
	if until, cooling := sessionCooldownModelsFromContext(ctx)[rescuer.Model]; cooling {
		rateLimitTurnFromContext(ctx).recordRescuePoolExhausted(rescuer.Model)
		observability.FromContext(ctx).Info("rescue pool exhausted, readmitting cooling-down model",
			"failed_model", failed.Model,
			"rescue_pool_readmitted", rescuer.Model,
			"demotion_expires_at", until.UTC().Format(time.RFC3339),
		)
		return
	}
	if !slices.Contains(sessionStrikeReadmitModelsFromContext(ctx), rescuer.Model) {
		return
	}
	rateLimitTurnFromContext(ctx).recordRescuePoolExhausted(rescuer.Model)
	observability.FromContext(ctx).Warn("rescue pool exhausted, readmitting session-demoted model",
		"failed_model", failed.Model,
		"rescue_pool_readmitted", rescuer.Model,
	)
}

// walkRescueCandidates resolves reachable candidates, preserving roster order
// for policy rescue and preferring cross-provider candidates for legacy rescue.
func walkRescueCandidates(
	failed router.Decision,
	candidates []string,
	reason string,
	excludedModels, automaticExcluded map[string]struct{},
	est, sigSavings, outputReserve int,
	providerFor func(id string) (string, bool),
) []router.Decision {
	var ordered, crossProvider, sameProvider []router.Decision
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
		provider, ok := providerFor(id)
		if !ok {
			continue
		}
		if !siblingFitsContext(id, provider, est, sigSavings, outputReserve) {
			continue
		}
		candidate := rescueDecisionFor(failed, id, provider, reason)
		ordered = append(ordered, candidate)
		if provider == failed.Provider {
			sameProvider = append(sameProvider, candidate)
			continue
		}
		crossProvider = append(crossProvider, candidate)
	}
	if reason == ReasonSiblingFailover && failed.Metadata != nil && failed.Metadata.RosterFailover {
		return ordered
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
	providerFor := func(id string) (string, bool) {
		return gatewayProviderFor(id, custom, gw)
	}
	return s.rescueWalkOrReadmitCooling(ctx, failed, candidates, reason, excludedModels, automaticExcluded, est, sigSavings, outputReserve, providerFor)
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

// siblingCandidateOrder deduplicates the roster's eligible rescue order.
// Without a bounded roster, the scored pool and replayed pin's runner-up
// remain eligible after policy-preferred models.
func siblingCandidateOrder(md *router.RoutingMetadata) []string {
	merged := md.RescueModels
	if !md.RosterFailover {
		merged = slices.Concat(merged, md.CandidateModels, []string{md.PairedModel})
	}
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
		md.SelectedRosterArmID = ""
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
