package proxy

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	candidatefilter "weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
)

const ReasonSiblingFailover = "sibling_failover"

type rescueSkipReason string

const (
	rescueSkipExcluded          rescueSkipReason = "excluded"
	rescueSkipAutomaticExcluded rescueSkipReason = "automatic_excluded"
	rescueSkipImageLimit        rescueSkipReason = "image_limit"
	rescueSkipNoBinding         rescueSkipReason = "no_binding"
	rescueSkipContextWindow     rescueSkipReason = "context_window"
)

func rescueBasisForTurn(decision router.Decision, turn dispatch.RescueTurn) router.Decision {
	if decision.Metadata == nil && turn.StickyHit && !turn.HardPinned && turn.FreshMetadata != nil {
		metadata := *turn.FreshMetadata
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

func (s *Service) siblingFailoverDecisions(ctx context.Context, failed router.Decision, est, sigSavings, outputReserve int) []router.Decision {
	md := failed.Metadata
	if md == nil {
		return nil
	}
	return s.rescueDecisions(ctx, failed, siblingCandidateOrder(md), ReasonSiblingFailover, est, sigSavings, outputReserve)
}

func (s *Service) rescueDecision(ctx context.Context, failed router.Decision, candidates []string, reason string, est, sigSavings, outputReserve int) (router.Decision, bool) {
	decisions := s.rescueDecisions(ctx, failed, candidates, reason, est, sigSavings, outputReserve)
	if len(decisions) == 0 {
		return router.Decision{}, false
	}
	return decisions[0], true
}

func (s *Service) rescueDecisions(ctx context.Context, failed router.Decision, candidates []string, reason string, est, sigSavings, outputReserve int) []router.Decision {
	if gw := s.gatewayProvidersForRequest(ctx); len(gw) > 0 {
		return s.gatewayRescueDecisions(ctx, failed, candidates, reason, gw, est, sigSavings, outputReserve)
	}
	if s.credentials.KeyedProviders == nil {
		return nil
	}
	available := s.keyedProvidersExcluding(s.excludedProvidersForRequest(ctx))
	excludedModels := candidatefilter.MergeExcludedModels(s.excludedModelsForRequest(ctx), customToolExcludedModels(ctx))
	var candidateProviders map[string]string
	if failed.Metadata != nil {
		candidateProviders = failed.Metadata.CandidateProviders
	}
	automaticExcluded := s.rescueExcludedModels(ctx)
	imageCount := requestImageCountFromContext(ctx)
	providerFor := func(id string) (string, bool) {
		return siblingProvider(id, candidateProviders, available, imageCount)
	}
	return s.rescueWalkOrReadmitCooling(ctx, failed, candidates, reason, excludedModels, automaticExcluded, est, sigSavings, outputReserve, providerFor)
}

func (s *Service) rescueWalkOrReadmitCooling(
	ctx context.Context,
	failed router.Decision,
	candidates []string,
	reason string,
	excludedModels, automaticExcluded map[string]struct{},
	est, sigSavings, outputReserve int,
	providerFor func(id string) (string, bool),
) []router.Decision {
	var skipped []string
	decisions := walkRescueCandidates(failed, candidates, reason, requestImageCountFromContext(ctx), excludedModels, automaticExcluded, cortexGatewayProvidersForRequest(ctx), est, sigSavings, outputReserve, providerFor, func(model string, why rescueSkipReason) {
		skipped = append(skipped, model+":"+string(why))
	})
	if len(skipped) > 0 {
		observability.FromContext(ctx).Info("Rescue candidates skipped",
			"failed_model", failed.Model,
			"rescue_reason", reason,
			"context_estimate", est,
			"skipped", strings.Join(skipped, ","),
		)
	}
	decisions = append(decisions, s.readmitCoolingRescueCandidates(ctx, failed, candidates, reason, excludedModels, est, sigSavings, outputReserve, providerFor)...)
	if len(decisions) > 0 || reason != ReasonSiblingFailover {
		return decisions
	}
	if overloaded := slices.Sorted(maps.Keys(s.overloadOpenModels(ctx))); len(overloaded) > 0 {
		if decisions = s.readmissionWalk(ctx, failed, candidates, overloaded, reason, excludedModels, est, sigSavings, outputReserve, providerFor); len(decisions) > 0 {
			return decisions
		}
	}
	struck := strikesInRescuePool(failed, candidates, sessionStrikeReadmitModelsFromContext(ctx))
	if len(struck) == 0 {
		return nil
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
	readmitExcluded = candidatefilter.MergeExcludedModels(readmitExcluded, s.controlPlaneExcludedModels(ctx))
	for model := range s.overloadOpenModels(ctx) {
		if !slices.Contains(readmit, model) {
			readmitExcluded[model] = struct{}{}
		}
	}
	return walkRescueCandidates(failed, pool, reason, requestImageCountFromContext(ctx), excludedModels, readmitExcluded, cortexGatewayProvidersForRequest(ctx), est, sigSavings, outputReserve, providerFor, nil)
}

func rosterRescueAdmits(failed router.Decision, model string) bool {
	md := failed.Metadata
	if md == nil || !md.RosterFailover {
		return true
	}
	return slices.Contains(md.RescueModels, model) || slices.Contains(md.SidecarRescuePool, model)
}

func strikesInRescuePool(failed router.Decision, candidates, struck []string) []string {
	md := failed.Metadata
	if md == nil || md.RosterFailover {
		return struck
	}
	var out []string
	for _, model := range struck {
		if slices.Contains(candidates, model) || slices.Contains(md.SidecarRescuePool, model) {
			out = append(out, model)
		}
	}
	return out
}

func (s *Service) noteRescueReadmission(ctx context.Context, failed, rescuer router.Decision) {
	if until, cooling := sessionCooldownModelsFromContext(ctx)[rescuer.Model]; cooling {
		dispatch.ThrottleLedgerFromContext(ctx).RecordRescuePoolExhausted(rescuer.Model)
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
	dispatch.ThrottleLedgerFromContext(ctx).RecordRescuePoolExhausted(rescuer.Model)
	observability.FromContext(ctx).Warn("rescue pool exhausted, readmitting session-demoted model",
		"failed_model", failed.Model,
		"rescue_pool_readmitted", rescuer.Model,
	)
}

func walkRescueCandidates(
	failed router.Decision,
	candidates []string,
	reason string,
	imageCount int,
	excludedModels, automaticExcluded, cortexGateways map[string]struct{},
	est, sigSavings, outputReserve int,
	providerFor func(id string) (string, bool),
	skip func(model string, reason rescueSkipReason),
) []router.Decision {
	if skip == nil {
		skip = func(string, rescueSkipReason) {}
	}
	var ordered, crossProvider, sameProvider []router.Decision
	for _, id := range candidates {
		if id == "" || id == failed.Model {
			continue
		}
		if _, drop := excludedModels[id]; drop {
			skip(id, rescueSkipExcluded)
			continue
		}
		if _, disabled := automaticExcluded[id]; disabled {
			skip(id, rescueSkipAutomaticExcluded)
			continue
		}
		if !catalog.AcceptsImageCount(id, imageCount, nil) {
			skip(id, rescueSkipImageLimit)
			continue
		}
		provider, ok := providerFor(id)
		if !ok {
			skip(id, rescueSkipNoBinding)
			continue
		}
		if binding, found := catalog.BindingFor(id, provider); found && !binding.AcceptsImageCount(imageCount) {
			skip(id, rescueSkipImageLimit)
			continue
		}
		if !siblingFitsContext(cortexGateways, id, provider, est, sigSavings, outputReserve) {
			skip(id, rescueSkipContextWindow)
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

func (s *Service) rescueExcludedModels(ctx context.Context) map[string]struct{} {
	excluded := slices.Concat(sessionDemotedModelsFromContext(ctx), observedOverflowModelsFromContext(ctx))
	probeDenied, _ := ctx.Value(SessionCooldownProbeDeniedModelsContextKey{}).([]string)
	if len(excluded) == 0 && len(probeDenied) == 0 {
		return s.globalAutomaticExcludedModels(ctx)
	}
	session := make(map[string]struct{}, len(excluded)+len(probeDenied))
	for _, model := range excluded {
		session[model] = struct{}{}
	}
	for _, model := range probeDenied {
		session[model] = struct{}{}
	}
	return candidatefilter.MergeExcludedModels(session, s.globalAutomaticExcludedModels(ctx))
}

func (s *Service) gatewayRescueDecisions(ctx context.Context, failed router.Decision, candidates []string, reason string, gw map[string]struct{}, est, sigSavings, outputReserve int) []router.Decision {
	custom := s.customBindingsForRequest(ctx)
	excludedModels := candidatefilter.MergeExcludedModels(s.excludedModelsForRequest(ctx), customToolExcludedModels(ctx))
	automaticExcluded := s.rescueExcludedModels(ctx)
	providerFor := func(id string) (string, bool) {
		return gatewayProviderFor(id, custom, gw)
	}
	return s.rescueWalkOrReadmitCooling(ctx, failed, candidates, reason, excludedModels, automaticExcluded, est, sigSavings, outputReserve, providerFor)
}

func (s *Service) subscriptionSiblingAllowed(ctx context.Context) bool {
	if s.credentials.BYOKOnly || len(credentials.ExternalKeysFromContext(ctx)) > 0 {
		return false
	}
	creds := CredentialsFromContext(ctx)
	return creds != nil && creds.OAuth
}

func rescueCredentialContext(ctx context.Context, rescueProvider string) context.Context {
	creds := CredentialsFromContext(ctx)
	if creds == nil || !creds.OAuth || oauthCredentialProvider(creds) == rescueProvider {
		return ctx
	}
	return clearCredentials(ctx)
}

func oauthCredentialProvider(creds *Credentials) string {
	if creds.Source == credSourceCodexSubscription {
		return providers.ProviderOpenAI
	}
	return providers.ProviderAnthropic
}

func (s *Service) gatewaySiblingAllowed(ctx context.Context, sibling router.Decision) bool {
	_, held := s.gatewayProvidersForRequest(ctx)[sibling.Provider]
	return held
}

func (s *Service) siblingRescueAllowed(ctx context.Context, gatewayHeld bool) bool {
	return !claudeEnterpriseTurn(ctx) && s.ResolveSiblingFailover(ctx) &&
		(s.shouldFailover(ctx) || s.subscriptionSiblingAllowed(ctx) || gatewayHeld) &&
		(!paidFallbackForbidden(ctx) || subscriptionStateModelsEnabled(ctx))
}

func gatewayProviderFor(model string, custom map[string][]string, gw map[string]struct{}) (string, bool) {
	for _, p := range custom[model] {
		if _, held := gw[p]; held {
			return p, true
		}
	}
	return "", false
}

func siblingFitsContext(cortexGateways map[string]struct{}, model, provider string, est, sigSavings, outputReserve int) bool {
	if est <= 0 {
		return true
	}
	needed := est + outputReserve
	if sigSavings > 0 && modelStripsAnthropicSignatures(model) {
		needed -= sigSavings
	}
	return needed <= gatewayContextWindow(cortexGateways, model, provider)
}

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

func siblingProvider(model string, resolved map[string]string, available map[string]struct{}, imageCount int) (string, bool) {
	if p, ok := resolved[model]; ok && p != "" {
		if _, keyed := available[p]; keyed {
			binding, known := catalog.BindingFor(model, p)
			if !known || binding.AcceptsImageCount(imageCount) {
				return p, true
			}
		}
	}
	if imageCount > 0 {
		for _, binding := range catalog.EnumerateBindings(model, available) {
			if binding.AcceptsImageCount(imageCount) {
				return binding.Provider, true
			}
		}
		return "", false
	}
	binding, ok := catalog.ResolveBinding(model, available)
	if !ok {
		return "", false
	}
	return binding.Provider, true
}

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

func (s *Service) keyedProvidersExcluding(excluded map[string]struct{}) map[string]struct{} {
	if s.credentials.KeyedProviders == nil {
		return nil
	}
	out := make(map[string]struct{}, len(s.credentials.KeyedProviders))
	for p := range s.credentials.KeyedProviders {
		if _, drop := excluded[p]; drop {
			continue
		}
		out[p] = struct{}{}
	}
	return out
}
