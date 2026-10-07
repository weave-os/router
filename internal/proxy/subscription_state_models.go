package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/policy"
)

type subscriptionStateAllowedModelsKey struct{}

func subscriptionStateModelsEnabled(ctx context.Context) bool {
	return !subscriptionFundingOutOfPlayForRequest(ctx) &&
		(len(installationSubscriptionModelsWhenActiveFromContext(ctx)) > 0 ||
			len(installationSubscriptionModelsWhenInactiveFromContext(ctx)) > 0)
}

// subscriptionStateRequest preserves all request safety constraints while
// narrowing eligibility. An empty configured state refuses rather than widening.
func (s *Service) subscriptionStateRequest(ctx context.Context, req router.Request, models []string) (context.Context, router.Request) {
	allowed := make(map[string]struct{})
	for _, model := range models {
		if _, excluded := req.ExcludedModels[model]; excluded {
			continue
		}
		if _, excluded := req.SafetyExcludedModels[model]; excluded {
			continue
		}
		if _, excluded := req.UnsignedHistoryExcludedModels[model]; excluded {
			continue
		}
		if req.AllowedModels != nil {
			if _, permitted := req.AllowedModels[model]; !permitted {
				continue
			}
		}
		allowed[model] = struct{}{}
	}
	ctx = context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, allowed)
	req.AllowedModels = allowed
	req.ExcludedModels = mergeExcludedModels(req.ExcludedModels, s.excludedModelsForRequest(ctx))
	return ctx, req
}

func (s *Service) subscriptionStateModelAvailable(ctx context.Context, req router.Request, headers http.Header, model string) bool {
	entry, known := catalog.ByID(model)
	if !known {
		return false
	}
	for _, binding := range entry.Providers {
		if req.EnabledProviders != nil {
			if _, enabled := req.EnabledProviders[binding.Provider]; !enabled {
				continue
			}
		}
		if !s.supportsSubscriptionTransport(binding.Provider) {
			continue
		}
		if managedSubscriptionCanServe(ctx, binding.Provider, model) {
			return true
		}
		resolved := s.resolveCredentials(clearCredentials(ctx), binding.Provider, model, headers)
		if !servedOnSubscription(resolved) {
			continue
		}
		if binding.Provider == providers.ProviderAnthropic && !s.claudeSubscriptionExhausted(ctx, headers) ||
			binding.Provider == providers.ProviderOpenAI && !s.codexSubscriptionExhausted(ctx, headers) {
			return true
		}
	}
	return false
}

// dispatchSubscriptionStateModels keeps model eligibility separate from funding:
// each active target must actually use a subscription. Only the exhausted set
// authorizes a paid attempt, even after a live quota rejection or stale pin.
func (s *Service) dispatchSubscriptionStateModels(ctx context.Context, in failoverInputs) (int, error) {
	request := *in.stateRequest
	in.stateRequest = nil
	in.alternatives = nil
	active := make([]string, 0)
	for _, model := range installationSubscriptionModelsWhenActiveFromContext(ctx) {
		if s.subscriptionStateModelAvailable(ctx, request, in.stateHeaders, model) {
			active = append(active, model)
		}
	}
	budget, cancel := context.WithTimeout(ctx, sameBindingRetryBudget)
	defer cancel()
	ctx = context.WithValue(ctx, subscriptionRotationBudgetKey{}, budget)
	fixed := request.ForceModel != "" || in.origin == policy.OverrideSourceDeployment || in.origin == policy.OverrideSourceRequest
	for len(active) > 0 && budget.Err() == nil {
		attemptCtx, attemptReq := s.subscriptionStateRequest(ctx, request, active)
		attemptCtx = context.WithValue(attemptCtx, subscriptionOnlyAttemptKey{}, true)
		attemptReq.EnabledProviders = make(map[string]struct{})
		for provider := range request.EnabledProviders {
			if s.supportsSubscriptionTransport(provider) {
				attemptReq.EnabledProviders[provider] = struct{}{}
			}
		}
		target := in.initialDecision
		if !fixed {
			if provider, engaged := s.usageBypassEngaged(ctx, in.stateHeaders, attemptReq); engaged {
				target.Model = request.RequestedModel
				target.Provider = provider
				target.Reason = reasonUsageBypass
				target.Metadata = nil
			}
		}
		_, permitted := attemptReq.AllowedModels[target.Model]
		permitted = permitted && s.supportsSubscriptionTransport(target.Provider)
		if !permitted && !fixed {
			var err error
			target, err = s.Route(attemptCtx, attemptReq)
			if err != nil {
				if errors.Is(err, policy.ErrNoEligibleArm) || errors.Is(err, cluster.ErrAllowlistEmptiesPool) {
					break
				}
				return -1, err
			}
		}
		if _, permitted := attemptReq.AllowedModels[target.Model]; !permitted || !s.supportsSubscriptionTransport(target.Provider) {
			break
		}
		winner, err := s.dispatchStateTarget(attemptCtx, in, target, true)
		if err == nil || committed(in.buf) || ctx.Err() != nil {
			return winner, err
		}
		if !isSubscriptionPoolError(err) && !errors.Is(err, ErrCreditsExhaustedSubscriptionUnavailable) && !providers.IsRetryable(err) &&
			!codexSubscriptionModelRejected(err) && !anthropicSubscriptionModelRejected(err) && !codexOAuthCredentialRejected(err) && !anthropicOAuthCredentialRejected(err) {
			return winner, err
		}
		observability.FromContext(ctx).Info("Subscription model unavailable; selecting another included target", "model", target.Model, "provider", target.Provider)
		remaining := active[:0]
		for _, model := range active {
			if model != target.Model {
				remaining = append(remaining, model)
			}
		}
		active = remaining
		if fixed {
			break
		}
	}
	if billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		return -1, ErrSubscriptionPoolExhausted
	}
	paidCtx, paidReq := s.subscriptionStateRequest(ctx, request, installationSubscriptionModelsWhenInactiveFromContext(ctx))
	if len(paidReq.AllowedModels) == 0 {
		return -1, cluster.ErrAllowlistEmptiesPool
	}
	paidCtx = billing.ReleaseLinkedFirst(paidCtx)
	paidCtx = context.WithValue(paidCtx, subscriptionAPIOnlyKey{}, true)
	paidCtx = withSuppressedClaudeSubscription(withSuppressedCodexSubscription(paidCtx))
	target := in.initialDecision
	if _, permitted := paidReq.AllowedModels[target.Model]; !permitted {
		if fixed {
			return -1, cluster.ErrAllowlistEmptiesPool
		}
		var err error
		target, err = s.Route(paidCtx, paidReq)
		if err != nil {
			return -1, err
		}
	}
	if _, permitted := paidReq.AllowedModels[target.Model]; !permitted {
		return -1, cluster.ErrAllowlistEmptiesPool
	}
	observability.FromContext(ctx).Info("Included subscription targets unavailable; using exhausted model set", "model", target.Model, "provider", target.Provider)
	return s.dispatchStateTarget(paidCtx, in, target, false)
}

func (s *Service) dispatchStateTarget(ctx context.Context, in failoverInputs, target router.Decision, included bool) (int, error) {
	ctx = s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, in.stateHeaders)
	attempt, err := in.buildAlternative(target)
	if err != nil {
		return -1, err
	}
	if in.buf != nil {
		in.buf.Discard()
	}
	in.initialDecision = target
	in.attempt = attempt
	in.bindings = s.resolveBindingsForDispatch(ctx, target)
	if included {
		// A subscription-capable binding cannot rotate onto a paid gateway.
		in.bindings = []catalog.ProviderBinding{{Provider: target.Provider}}
		in.deferFlushOnExhaustion = true
	}
	winner, err := s.dispatchWithFallback(ctx, in)
	if err == nil && in.onAlternative != nil {
		in.onAlternative(target)
	}
	return winner, err
}
