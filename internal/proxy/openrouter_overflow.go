package proxy

import (
	"context"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
)

// openRouterOverflowEnabled reports whether the installation serves exhausted
// Claude/Codex subscriptions only through OpenRouter.
func openRouterOverflowEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySubscriptionExhaustedOpenRouterOnly, false)
}

// openRouterOverflowModels narrows the exhausted-state set to models OpenRouter
// serves; an unconfigured set admits every such model.
func openRouterOverflowModels(configured []string) []string {
	candidates := configured
	if len(candidates) == 0 {
		candidates = make([]string, 0, len(catalog.Models))
		for _, model := range catalog.Models {
			candidates = append(candidates, model.ID)
		}
	}
	out := make([]string, 0, len(candidates))
	for _, model := range candidates {
		if _, ok := catalog.BindingFor(model, providers.ProviderOpenRouter); ok {
			out = append(out, model)
		}
	}
	return out
}

// openRouterOverflowTarget moves a paid target onto its OpenRouter binding.
func openRouterOverflowTarget(ctx context.Context, target router.Decision) router.Decision {
	if !openRouterOverflowEnabled(ctx) || target.Provider == providers.ProviderOpenRouter {
		return target
	}
	if _, ok := catalog.BindingFor(target.Model, providers.ProviderOpenRouter); !ok {
		return target
	}
	target.Provider = providers.ProviderOpenRouter
	// Arm and binding metadata name the replaced provider's binding.
	target.Metadata = nil
	return target
}

// openRouterOverflowFallbackTarget picks the first allowed exhausted-state model
// OpenRouter can serve when routing cannot: overflow-only bindings are invisible
// to the router, so a model reachable only through them never routes.
func (s *Service) openRouterOverflowFallbackTarget(ctx context.Context, req router.Request, models []string, initial router.Decision) (router.Decision, bool) {
	if !openRouterOverflowEnabled(ctx) {
		return router.Decision{}, false
	}
	for _, model := range models {
		if _, allowed := req.AllowedModels[model]; !allowed {
			continue
		}
		target := initial
		target.Model = model
		target.Metadata = nil
		target = openRouterOverflowTarget(ctx, target)
		if len(s.openRouterOverflowBindings(ctx, target)) > 0 {
			return target, true
		}
	}
	return router.Decision{}, false
}

// openRouterOverflowBindings returns the only binding a paid overflow target
// may dispatch on, or nil when OpenRouter cannot serve it for this request.
func (s *Service) openRouterOverflowBindings(ctx context.Context, target router.Decision) []catalog.ProviderBinding {
	if target.Provider != providers.ProviderOpenRouter {
		return nil
	}
	binding, ok := catalog.BindingFor(target.Model, providers.ProviderOpenRouter)
	if !ok {
		return nil
	}
	if _, excluded := s.excludedProvidersForRequest(ctx)[providers.ProviderOpenRouter]; excluded {
		return nil
	}
	if s.deploymentKeyedProviders != nil {
		_, keyed := s.deploymentKeyedProviders[providers.ProviderOpenRouter]
		if !keyed && BuildCredentialsMap(externalKeysFromContext(ctx))[providers.ProviderOpenRouter] == nil {
			return nil
		}
	}
	return []catalog.ProviderBinding{binding}
}

// subscriptionStateTargetBindings keeps included-subscription attempts on their
// native binding walk and pins every paid overflow attempt to OpenRouter.
func (s *Service) subscriptionStateTargetBindings(ctx context.Context, target router.Decision) []catalog.ProviderBinding {
	if !openRouterOverflowEnabled(ctx) || subscriptionAttemptOnly(ctx) {
		return s.resolveBindingsForDispatch(ctx, target)
	}
	return s.openRouterOverflowBindings(ctx, target)
}

// withOpenRouterOverflowTarget rewrites a paid dispatch outside the
// subscription-state lane onto OpenRouter.
func (s *Service) withOpenRouterOverflowTarget(ctx context.Context, in failoverInputs) (context.Context, failoverInputs, error) {
	target := openRouterOverflowTarget(ctx, in.initialDecision)
	bindings := s.openRouterOverflowBindings(ctx, target)
	if len(bindings) == 0 {
		return ctx, in, cluster.ErrAllowlistEmptiesPool
	}
	if in.initialDecision.Provider != target.Provider {
		if in.buildAlternative == nil {
			return ctx, in, cluster.ErrAllowlistEmptiesPool
		}
		attempt, err := in.buildAlternative(target)
		if err != nil {
			return ctx, in, err
		}
		in.attempt = attempt
		ctx = s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, in.subscriptionStateHeaders)
		if in.onSubscriptionStateTarget != nil {
			in.onSubscriptionStateTarget(target, bindings)
		}
	}
	in.initialDecision = target
	in.bindings = bindings
	return ctx, in, nil
}
