package proxy

import (
	"context"
	"slices"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

// compactionModelOrDefault returns the configured Sonnet-class model for
// Claude Code's own compaction turn.
func (s *Service) compactionModelOrDefault() string {
	if s.compactionModel != "" {
		return s.compactionModel
	}
	return policy.PrecompactionDefaultModel
}

// anthropicSummarizerEligible reports whether model is a reviewed member of
// the precompaction-summary policy: an Anthropic-served, non-low-tier catalog
// model Claude Code's own compaction turn may be pinned to.
func anthropicSummarizerEligible(model string) bool {
	spec, ok := policy.DefaultRegistry().Spec(policy.PurposePrecompactionSummary)
	if !ok || !slices.Contains(spec.FixedCatalogModels, model) {
		return false
	}
	m, ok := catalog.ByID(model)
	if !ok || m.Tier == catalog.TierLow {
		return false
	}
	for _, b := range m.Providers {
		if b.Provider == providers.ProviderAnthropic {
			return true
		}
	}
	return false
}

// compactionPreferredSummarizer returns the session's active pinned model
// when it is served by Anthropic directly or through a gateway — the same
// model that has been running the conversation, so its prompt cache is warm
// for the summary call. Empty when there is no pin store, no active pin, or
// the pin is on another vendor.
func (s *Service) compactionPreferredSummarizer(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) string {
	if s.pinStore == nil {
		return ""
	}
	pin, active := s.loadPin(ctx, sessionKey, role)
	if !active || (pin.Provider != providers.ProviderAnthropic && !providers.IsGateway(pin.Provider)) {
		return ""
	}
	return pin.Model
}

// compactionSummarizer is a model a harness compaction turn may be served on,
// with the override source that authorizes it.
type compactionSummarizer struct {
	provider string
	model    string
	source   policy.OverrideSource
}

// compactionHardPin picks the model for a harness's own compaction turn: the
// first of compactionSummarizers. ok=false when nothing is eligible for this
// request, so the caller falls back to the generic hard-pin tier.
func (s *Service) compactionHardPin(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) (provider, model string, source policy.OverrideSource, ok bool) {
	summarizers := s.compactionSummarizers(ctx, sessionKey, role, req)
	if len(summarizers) == 0 {
		return "", "", "", false
	}
	return summarizers[0].provider, summarizers[0].model, summarizers[0].source, true
}

// compactionSummarizers lists, in preference order, the models a harness
// compaction turn may be served on: the model that ran the conversation when
// it is Sonnet-class or better (prompt cache warm — what Claude Code and Codex
// do against their vendor directly), then the configured compaction model,
// then the large-window summarizer. A Codex thread arrives in Responses
// format, so when a non-Anthropic model has been serving it that model leads
// rather than a cross-format Sonnet summary — the summary replaces the
// thread's history for every turn that follows. Anthropic summarizers bind
// directly or through a gateway key the request holds. source records whether
// the session's own model or the deployment's configured one chose each
// entry, so the plan is authorized under the matching override.
func (s *Service) compactionSummarizers(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) []compactionSummarizer {
	var out []compactionSummarizer
	if req.ClientApp == ClientAppCodex {
		if p, m, served := s.compactionSessionModel(ctx, sessionKey, role, req); served {
			out = append(out, compactionSummarizer{provider: p, model: m, source: policy.OverrideSourceSession})
		}
	}
	cortexGateways := cortexGatewayProvidersForRequest(ctx)
	eligible := func(m string) bool {
		if !anthropicSummarizerEligible(m) {
			return false
		}
		provider, bound := summarizerProvider(m, req)
		if !bound {
			return false
		}
		if s.availableModels != nil {
			if _, available := s.availableModels[m]; !available {
				return false
			}
		}
		if _, excluded := req.ExcludedModels[m]; excluded {
			return false
		}
		// Gateways may enforce a smaller window than the vendor, so the serving binding's window is the one that counts.
		return !automaticallyDisabled(req, m) &&
			(req.EstimatedInputTokens <= 0 || req.EstimatedInputTokens+contextWindowOutputReserve <= gatewayContextWindow(cortexGateways, m, provider))
	}
	cascade := []struct {
		model  string
		source policy.OverrideSource
	}{
		{s.compactionPreferredSummarizer(ctx, sessionKey, role), policy.OverrideSourceSession},
		{s.compactionModelOrDefault(), policy.OverrideSourceDeployment},
		{policy.PrecompactionLargeWindowModel, policy.OverrideSourceDeployment},
	}
	for _, step := range cascade {
		latest := catalog.LatestInFamily(step.model, eligible)
		if latest == "" || slices.ContainsFunc(out, func(c compactionSummarizer) bool { return c.model == latest }) {
			continue
		}
		provider, _ := summarizerProvider(latest, req)
		out = append(out, compactionSummarizer{provider: provider, model: latest, source: step.source})
	}
	return out
}

// compactionCrossProviderRescuers lists the request's gateway-held models on
// another gateway than the primary summarizer's, highest tier then widest
// window first. Every summarizer behind one gateway shares a tokenizer and
// window, so an overflow there is only rescuable across gateways: Claude
// counts the same transcript as roughly 1.5x the tokens gpt-6-luna does, so a
// nominally smaller window can still fit it.
func (s *Service) compactionCrossProviderRescuers(ctx context.Context, primary compactionSummarizer, req router.Request) []compactionSummarizer {
	cortexGateways := cortexGatewayProvidersForRequest(ctx)
	var out []compactionSummarizer
	for model := range req.CustomBindings {
		provider, held := gatewayProviderFor(model, req.CustomBindings, req.GatewayProviders)
		if !held || provider == primary.provider || catalog.TierFor(model) < catalog.TierMid {
			continue
		}
		if _, excluded := req.ExcludedModels[model]; excluded || automaticallyDisabled(req, model) {
			continue
		}
		if s.availableModels != nil {
			if _, available := s.availableModels[model]; !available {
				continue
			}
		}
		out = append(out, compactionSummarizer{provider: provider, model: model, source: policy.OverrideSourceDeployment})
	}
	slices.SortFunc(out, func(a, b compactionSummarizer) int {
		if ta, tb := catalog.TierFor(a.model), catalog.TierFor(b.model); ta != tb {
			return int(tb) - int(ta)
		}
		if wa, wb := gatewayContextWindow(cortexGateways, a.model, a.provider), gatewayContextWindow(cortexGateways, b.model, b.provider); wa != wb {
			return wb - wa
		}
		return strings.Compare(a.model, b.model)
	})
	return out
}

// summarizerProvider binds an Anthropic summarizer for this request: through
// a held gateway key that aliases it for a gateway-exclusive tenant, else
// Anthropic directly when the tenant enables it.
func summarizerProvider(model string, req router.Request) (string, bool) {
	if len(req.GatewayProviders) > 0 {
		return gatewayProviderFor(model, req.CustomBindings, req.GatewayProviders)
	}
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[providers.ProviderAnthropic]; !enabled {
			return "", false
		}
	}
	return providers.ProviderAnthropic, true
}

// compactionRescueMetadata gives a compaction hard pin an ordered rescue list:
// the remaining summarizers, then the generic utility model, then models on
// other gateways for an overflow. Without it a pre-commit failure on the summarizer
// surfaces to the client, which retries the same compaction against the same
// failing model.
func compactionRescueMetadata(fallbacks []compactionSummarizer, utilityProvider, utilityModel string, crossProvider []compactionSummarizer) *router.RoutingMetadata {
	md := &router.RoutingMetadata{RosterFailover: true, CandidateProviders: map[string]string{}}
	add := func(provider, model string) {
		if model == "" || slices.Contains(md.RescueModels, model) {
			return
		}
		md.RescueModels = append(md.RescueModels, model)
		md.CandidateProviders[model] = provider
	}
	for _, fallback := range fallbacks {
		add(fallback.provider, fallback.model)
	}
	add(utilityProvider, utilityModel)
	for _, rescuer := range crossProvider {
		add(rescuer.provider, rescuer.model)
	}
	if len(md.RescueModels) == 0 {
		return nil
	}
	return md
}

// compactionSessionModel upgrades the last served non-Anthropic session
// family to its newest eligible catalog version. The active thread pin and
// HMM history compete by completion time; Anthropic uses its separate path.
func (s *Service) compactionSessionModel(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) (provider, model string, ok bool) {
	if s.pinStore == nil {
		return "", "", false
	}
	threadPin, active := s.loadPin(ctx, sessionKey, role)
	if !active {
		threadPin = sessionpin.Pin{}
	}
	served := latestServedPin(threadPin, s.loadHMMHistory(ctx, sessionKey, role))
	model = served.LastServedModel
	if model == "" {
		model = served.Model
	}
	model, _ = hmm.SplitEffort(model)
	model = catalog.LatestInFamily(model, func(candidate string) bool {
		m, known := catalog.ByID(candidate)
		if !known || m.Tier == catalog.TierLow {
			return false
		}
		binding, bound := s.servedBinding(candidate, served.Provider, req)
		if !bound || binding.Provider == providers.ProviderAnthropic {
			return false
		}
		if s.availableModels != nil {
			if _, available := s.availableModels[candidate]; !available {
				return false
			}
		}
		if _, excluded := req.ExcludedModels[candidate]; excluded {
			return false
		}
		return !automaticallyDisabled(req, candidate)
	})
	if model == "" {
		return "", "", false
	}
	binding, bound := s.servedBinding(model, served.Provider, req)
	if !bound {
		return "", "", false
	}
	return binding.Provider, model, true
}

// latestServedPin picks the pin whose last turn ended most recently among
// those that record a served model.
func latestServedPin(pins ...sessionpin.Pin) sessionpin.Pin {
	var latest sessionpin.Pin
	found := false
	for _, pin := range pins {
		if pin.LastServedModel == "" && pin.Model == "" {
			continue
		}
		if !found || pin.LastTurnEndedAt.After(latest.LastTurnEndedAt) {
			latest = pin
			found = true
		}
	}
	return latest
}

// servedBinding resolves the provider binding for a session's served model:
// the provider that served it when that provider is still enabled and binds
// the model, else the first enabled binding (a failed turn may leave the
// stored provider stale).
func (s *Service) servedBinding(model, servedProvider string, req router.Request) (catalog.ProviderBinding, bool) {
	providerSet := req.EnabledProviders
	if providerSet == nil {
		providerSet = s.clients.NameSet()
	}
	if _, enabled := providerSet[servedProvider]; enabled {
		pinned := map[string]struct{}{servedProvider: {}}
		if binding, valid := catalog.ResolveBindingWithCustom(model, pinned, req.CustomBindings); valid {
			return binding, true
		}
	}
	return catalog.ResolveBindingWithCustom(model, providerSet, req.CustomBindings)
}
