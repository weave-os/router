package proxy

import (
	"context"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func openRouterOverflowEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeySubscriptionExhaustedOpenRouterOnly, false)
}

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

func openRouterOverflowTarget(ctx context.Context, target router.Decision) router.Decision {
	if !openRouterOverflowEnabled(ctx) || target.Provider == providers.ProviderOpenRouter {
		return target
	}
	if _, ok := catalog.BindingFor(target.Model, providers.ProviderOpenRouter); !ok {
		return target
	}
	target.Provider = providers.ProviderOpenRouter
	target.Metadata = nil
	return target
}

func (s *Service) openRouterOverflowFallbackTarget(ctx context.Context, req router.Request, models []string, initial router.Decision) (router.Decision, bool) {
	if !openRouterOverflowEnabled(ctx) {
		return router.Decision{}, false
	}
	for _, model := range models {
		if _, allowed := req.Candidates.AllowedModels[model]; !allowed {
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

func (s *Service) openRouterOverflowBindings(ctx context.Context, target router.Decision) []catalog.ProviderBinding {
	if target.Provider != providers.ProviderOpenRouter {
		return nil
	}
	binding, ok := catalog.BindingFor(target.Model, providers.ProviderOpenRouter)
	if !ok {
		return nil
	}
	if !binding.AcceptsImageCount(requestcontext.RequestImageCountFromContext(ctx)) {
		return nil
	}
	if _, excluded := s.excludedProvidersForRequest(ctx)[providers.ProviderOpenRouter]; excluded {
		return nil
	}
	if s.credentials.KeyedProviders != nil {
		_, keyed := s.credentials.KeyedProviders[providers.ProviderOpenRouter]
		if !keyed && credentials.BuildCredentialsMap(credentials.ExternalKeysFromContext(ctx))[providers.ProviderOpenRouter] == nil {
			return nil
		}
	}
	return []catalog.ProviderBinding{binding}
}

func (s *Service) subscriptionStateTargetBindings(ctx context.Context, target router.Decision) []catalog.ProviderBinding {
	if !openRouterOverflowEnabled(ctx) || credentials.SubscriptionAttemptOnly(ctx) {
		return s.resolveBindingsForDispatch(ctx, target)
	}
	return s.openRouterOverflowBindings(ctx, target)
}

func (s *Service) withOpenRouterOverflowTarget(ctx context.Context, in dispatch.FailoverInputs) (context.Context, dispatch.FailoverInputs, error) {
	target := openRouterOverflowTarget(ctx, in.InitialDecision)
	bindings := s.openRouterOverflowBindings(ctx, target)
	if len(bindings) == 0 {
		return ctx, in, router.ErrAllowlistEmptiesPool
	}
	if in.InitialDecision.Provider != target.Provider {
		if in.BuildAlternative == nil {
			return ctx, in, router.ErrAllowlistEmptiesPool
		}
		attempt, err := in.BuildAlternative(target)
		if err != nil {
			return ctx, in, err
		}
		in.Attempt = attempt
		ctx = s.resolveCredentials(credentials.ClearCredentials(ctx), target.Provider, target.Model, in.SubscriptionStateHeaders)
		if in.OnSubscriptionStateTarget != nil {
			in.OnSubscriptionStateTarget(target, bindings)
		}
	}
	in.InitialDecision = target
	in.Bindings = bindings
	return ctx, in, nil
}
