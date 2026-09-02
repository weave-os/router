package proxy

import (
	"context"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/catalog"
)

const codexOAuthPassthroughReason = "codex_oauth_passthrough"

// codexOAuthPassthroughModel returns the native Codex model requested by a
// validated subscription request, or an empty string when normal routing
// should decide the model.
func codexOAuthPassthroughModel(ctx context.Context) string {
	model, _ := ctx.Value(codexOAuthPassthroughModelContextKey{}).(string)
	return model
}

// codexOAuthPassthroughDecision returns a direct Codex decision for a native
// Codex subscription model. Cluster artifacts contain automatic routing
// candidates, not necessarily every model that can be served by a caller's
// subscription, so this path must not depend on the artifact roster.
func (s *Service) codexOAuthPassthroughDecision(ctx context.Context, req router.Request) (router.Decision, bool) {
	model := codexOAuthPassthroughModel(ctx)
	if model == "" {
		return router.Decision{}, false
	}
	if !codexSubscriptionCoversModel(model) {
		return router.Decision{}, false
	}
	if req.RequestedModel != model {
		return router.Decision{}, false
	}
	if req.ForceModel != "" {
		return router.Decision{}, false
	}
	provider := providers.ProviderCodex
	if _, registered := s.providers[provider]; !registered {
		provider = providers.ProviderOpenAI
	}
	if req.EnabledProviders != nil {
		if _, enabled := req.EnabledProviders[provider]; !enabled {
			// Older compositions may only register/enroll the OpenAI adapter;
			// retain that fallback while the production composition uses Codex.
			if provider == providers.ProviderCodex {
				if _, enabled := req.EnabledProviders[providers.ProviderOpenAI]; !enabled {
					return router.Decision{}, false
				}
				provider = providers.ProviderOpenAI
			} else {
				return router.Decision{}, false
			}
		}
	}
	if _, excluded := req.ExcludedModels[model]; excluded {
		return router.Decision{}, false
	}
	if req.AllowedModels != nil {
		if _, allowed := req.AllowedModels[model]; !allowed {
			return router.Decision{}, false
		}
	}
	if _, excluded := req.SafetyExcludedModels[model]; excluded {
		return router.Decision{}, false
	}
	if _, registered := s.providers[provider]; !registered {
		return router.Decision{}, false
	}
	if binding, ok := catalog.ResolveBinding(model, map[string]struct{}{provider: {}}); !ok || binding.Provider != provider {
		return router.Decision{}, false
	}
	return router.Decision{
		Provider:         provider,
		Model:            model,
		Reason:           codexOAuthPassthroughReason,
		CredentialSource: router.CredentialSourceCodexOAuth,
	}, true
}

// applyCodexOAuthPassthrough records a native Codex fallback on res.
func (s *Service) applyCodexOAuthPassthrough(ctx context.Context, req router.Request, res *turnLoopResult) bool {
	decision, ok := s.codexOAuthPassthroughDecision(ctx, req)
	if !ok {
		return false
	}
	res.Decision = decision
	res.Fresh = decision
	res.UsageBypass = true
	res.PinTier = codexOAuthPassthroughReason
	return true
}
