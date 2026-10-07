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
	"weave-os/router/internal/translate"
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
		if _, excluded := req.AutomaticExcludedModels[model]; excluded && model != req.ForceModel {
			continue
		}
		if _, excluded := req.SafetyExcludedModels[model]; excluded {
			continue
		}
		if _, excluded := req.UnsignedHistoryExcludedModels[model]; excluded {
			continue
		}
		if req.AllowedModels != nil && model != req.ForceModel {
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
		if _, excluded := s.excludedProvidersForRequest(ctx)[binding.Provider]; excluded {
			continue
		}
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
		resolvedCredentials := s.resolveCredentials(clearCredentials(ctx), binding.Provider, model, headers)
		if !servedOnSubscription(resolvedCredentials) {
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
	request := *in.subscriptionStateRequest
	sessionUnavailableModels := modelSet(sessionDemotedModelsFromContext(ctx))
	if sessionUnavailableModels == nil {
		sessionUnavailableModels = make(map[string]struct{})
	}
	for model, cooldownUntil := range sessionCooldownModelsFromContext(ctx) {
		if cooldownUntil.After(s.clockNow()) {
			sessionUnavailableModels[model] = struct{}{}
		}
	}
	request.AutomaticExcludedModels = mergeExcludedModels(request.AutomaticExcludedModels, sessionUnavailableModels)
	if request.ForceModel == "" && in.initialDecision.Reason == translate.ReasonUserForceModel {
		// runTurnLoop already validated and readmitted a strict force-model pin;
		// preserve that exception when rebuilding the request for state routing.
		request.ForceModel = in.initialDecision.Model
	}
	in.subscriptionStateRequest = nil
	in.alternatives = nil
	activeModels := make([]string, 0)
	for _, model := range installationSubscriptionModelsWhenActiveFromContext(ctx) {
		if s.subscriptionStateModelAvailable(ctx, request, in.subscriptionStateHeaders, model) {
			activeModels = append(activeModels, model)
		}
	}
	budget, cancel := context.WithTimeout(ctx, sameBindingRetryBudget)
	defer cancel()
	ctx = context.WithValue(ctx, subscriptionRotationBudgetKey{}, budget)
	hasFixedTarget := request.ForceModel != "" || callerModelPassthroughActive(ctx) ||
		in.origin == policy.OverrideSourceDeployment || in.origin == policy.OverrideSourceRequest
	for len(activeModels) > 0 && budget.Err() == nil {
		attemptCtx, attemptReq := s.subscriptionStateRequest(ctx, request, activeModels)
		attemptCtx = context.WithValue(attemptCtx, subscriptionOnlyAttemptKey{}, true)
		attemptReq.EnabledProviders = make(map[string]struct{})
		excludedProviders := s.excludedProvidersForRequest(ctx)
		for provider := range request.EnabledProviders {
			_, excluded := excludedProviders[provider]
			if s.supportsSubscriptionTransport(provider) && !excluded {
				attemptReq.EnabledProviders[provider] = struct{}{}
			}
		}
		target := in.initialDecision
		if !hasFixedTarget {
			if provider, engaged := s.usageBypassEngaged(ctx, in.subscriptionStateHeaders, attemptReq); engaged {
				target.Model = request.RequestedModel
				target.Provider = provider
				target.Reason = reasonUsageBypass
				target.Metadata = nil
			}
		}
		_, permitted := attemptReq.AllowedModels[target.Model]
		permitted = permitted && s.supportsSubscriptionTransport(target.Provider)
		if !permitted && !hasFixedTarget {
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
		winner, err := s.dispatchSubscriptionStateTarget(attemptCtx, in, target, true)
		if err == nil || committed(in.buf) || ctx.Err() != nil {
			return winner, err
		}
		internalRotationExpired := subscriptionRotationExpired(ctx, budget)
		if !internalRotationExpired && !isSubscriptionPoolError(err) && !errors.Is(err, ErrCreditsExhaustedSubscriptionUnavailable) && !providers.IsRetryable(err) &&
			!codexSubscriptionModelRejected(err) && !anthropicSubscriptionModelRejected(err) && !codexOAuthCredentialRejected(err) && !anthropicOAuthCredentialRejected(err) {
			return winner, err
		}
		observability.FromContext(ctx).Info("Subscription model unavailable; selecting another included target", "model", target.Model, "provider", target.Provider)
		remainingModels := activeModels[:0]
		for _, model := range activeModels {
			if model != target.Model {
				remainingModels = append(remainingModels, model)
			}
		}
		activeModels = remainingModels
		if hasFixedTarget {
			break
		}
	}
	if billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		return -1, ErrSubscriptionPoolExhausted
	}
	paidModels := installationSubscriptionModelsWhenInactiveFromContext(ctx)
	paidCtx, paidReq := s.subscriptionStateRequest(ctx, request, paidModels)
	paidCtx = billing.ReleaseLinkedFirst(paidCtx)
	paidCtx = context.WithValue(paidCtx, subscriptionAPIOnlyKey{}, true)
	if subscriptionRotationExpired(ctx, budget) {
		paidCtx = context.WithValue(paidCtx, subscriptionRotationBudgetDisabledKey{}, true)
	}
	paidCtx = withSuppressedClaudeSubscription(withSuppressedCodexSubscription(paidCtx))
	target := in.initialDecision
	for {
		_, permitted := paidReq.AllowedModels[target.Model]
		if permitted && s.subscriptionStatePaidTargetAvailable(paidCtx, in.subscriptionStateHeaders, target) {
			break
		}
		if hasFixedTarget {
			return -1, cluster.ErrAllowlistEmptiesPool
		}
		if permitted {
			request.ExcludedModels = mergeExcludedModels(request.ExcludedModels, map[string]struct{}{target.Model: {}})
			paidCtx, paidReq = s.subscriptionStateRequest(paidCtx, request, paidModels)
		}
		if len(paidReq.AllowedModels) == 0 {
			return -1, cluster.ErrAllowlistEmptiesPool
		}
		var err error
		target, err = s.Route(paidCtx, paidReq)
		if err != nil {
			return -1, err
		}
	}
	observability.FromContext(ctx).Info("Included subscription targets unavailable; using exhausted model set", "model", target.Model, "provider", target.Provider)
	return s.dispatchSubscriptionStateTarget(paidCtx, in, target, false)
}

func subscriptionRotationExpired(ctx, budget context.Context) bool {
	return ctx.Err() == nil && budget.Err() != nil
}

func (s *Service) subscriptionStatePaidTargetAvailable(ctx context.Context, headers http.Header, target router.Decision) bool {
	resolvedCredentials := s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, headers)
	if servedOnSubscription(resolvedCredentials) || servedOnCodexSubscription(resolvedCredentials) {
		return false
	}
	return len(s.resolveBindingsForDispatch(resolvedCredentials, target)) > 0
}

func (s *Service) dispatchSubscriptionStateTarget(ctx context.Context, in failoverInputs, target router.Decision, usesIncludedSubscription bool) (int, error) {
	ctx = s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, in.subscriptionStateHeaders)
	resolvedTargetBindings := s.resolveBindingsForDispatch(ctx, target)
	if in.onSubscriptionStateTarget != nil {
		in.onSubscriptionStateTarget(target, resolvedTargetBindings)
	} else if in.onAlternative != nil {
		in.onAlternative(target)
	}
	if !usesIncludedSubscription && in.onSubscriptionStatePaidTarget != nil {
		in.onSubscriptionStatePaidTarget(target, resolvedTargetBindings)
	}
	attempt, err := in.buildAlternative(target)
	if err != nil {
		return -1, err
	}
	if in.buf != nil {
		in.buf.Discard()
	}
	in.initialDecision = target
	in.attempt = attempt
	in.bindings = resolvedTargetBindings
	if usesIncludedSubscription {
		// A subscription-capable binding cannot rotate onto a paid gateway.
		in.bindings = []catalog.ProviderBinding{{Provider: target.Provider}}
		in.deferFlushOnExhaustion = true
	}
	winner, err := s.dispatchWithFallback(ctx, in)
	return winner, err
}
