package proxy

import (
	"context"
	"errors"
	"slices"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

// ErrContextWindowExceeded is returned when a request cannot fit any eligible
// model's window. The router never rewrites client history to make it fit:
// the client receives its native prompt-too-long error and compacts itself.
var ErrContextWindowExceeded = errors.New("proxy: request context exceeds every eligible model's window")

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
// when it is served by Anthropic directly — the same model that has been
// running the conversation, so its prompt cache is warm for the summary call.
// Empty when there is no pin store, no active pin, or the pin is elsewhere.
func (s *Service) compactionPreferredSummarizer(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string) string {
	if s.pinStore == nil {
		return ""
	}
	pin, active := s.loadPin(ctx, sessionKey, role)
	if !active || pin.Provider != providers.ProviderAnthropic {
		return ""
	}
	return pin.Model
}

// compactionHardPin picks the model for a harness's own compaction turn: the
// model that ran the conversation when it is Sonnet-class or better (prompt
// cache warm — what Claude Code and Codex do against their vendor directly),
// else the configured compaction model on Anthropic. A Codex thread arrives
// in Responses format, so when a non-Anthropic model has been serving it the
// turn stays there rather than being summarized cross-format by Sonnet — the
// summary replaces the thread's history for every turn that follows. source
// records whether the session's own model or the deployment's configured one
// won, so the plan is authorized under the matching override. ok=false when
// nothing is eligible for this request, so the caller falls back to the
// generic hard-pin tier.
func (s *Service) compactionHardPin(ctx context.Context, sessionKey [sessionpin.SessionKeyLen]byte, role string, req router.Request) (provider, model string, source policy.OverrideSource, ok bool) {
	// Gateway-exclusive tenants drop vendor bindings; leave them to the resolver.
	if len(req.GatewayProviders) > 0 {
		return "", "", "", false
	}
	if req.ClientApp == ClientAppCodex {
		if p, m, served := s.compactionSessionModel(ctx, sessionKey, role, req); served {
			return p, m, policy.OverrideSourceSession, true
		}
	}
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[providers.ProviderAnthropic]; !enabled {
			return "", "", "", false
		}
	}
	eligible := func(m string) bool {
		if !anthropicSummarizerEligible(m) {
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
		return !automaticallyDisabled(req, m)
	}
	preferred := s.compactionPreferredSummarizer(ctx, sessionKey, role)
	if latest := catalog.LatestInFamily(preferred, eligible); latest != "" {
		return providers.ProviderAnthropic, latest, policy.OverrideSourceSession, true
	}
	if m := catalog.LatestInFamily(s.compactionModelOrDefault(), eligible); m != "" {
		return providers.ProviderAnthropic, m, policy.OverrideSourceDeployment, true
	}
	return "", "", "", false
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
