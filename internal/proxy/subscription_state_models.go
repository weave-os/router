package proxy

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"

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
	enabled, _ := ctx.Value(InstallationSubscriptionStateModelsEnabledContextKey{}).(bool)
	return !subscriptionFundingOutOfPlayForRequest(ctx) &&
		(enabled || openRouterOverflowEnabled(ctx) ||
			len(installationSubscriptionModelsWhenActiveFromContext(ctx)) > 0 ||
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
// readmitSessionStrikes drops the session's struck models from the request's
// exclusions: the last resort once every healthy included model failed,
// mirroring sibling rescue's readmission.
func readmitSessionStrikes(ctx context.Context, request *router.Request) bool {
	readmit := sessionStrikeReadmitModelsFromContext(ctx)
	if len(readmit) == 0 {
		return false
	}
	excluded := maps.Clone(request.AutomaticExcludedModels)
	for _, model := range readmit {
		delete(excluded, model)
	}
	request.AutomaticExcludedModels = excluded
	return true
}

func (s *Service) dispatchSubscriptionStateModels(ctx context.Context, in failoverInputs) (int, error) {
	request := *in.subscriptionStateRequest
	// The paid-target check below authorizes before dispatchPlanned marks the turn.
	ctx = withClientAppRefusalPaidFallback(ctx, in.initialDecision)
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
	includedModels := s.includedSubscriptionModels(ctx, request, in.subscriptionStateHeaders)
	ctx, rotationBudget := withSubscriptionAdmissionBudget(ctx)
	hasFixedTarget := request.ForceModel != "" || callerModelPassthroughActive(ctx) ||
		in.origin == policy.OverrideSourceDeployment || in.origin == policy.OverrideSourceRequest
	readmitted := false
	for len(includedModels) > 0 && rotationBudget.remaining > 0 {
		attemptCtx, attemptReq := s.subscriptionStateRequest(ctx, request, includedModels)
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
				if errors.Is(err, policy.ErrNoEligibleArm) || errors.Is(err, cluster.ErrAllowlistEmptiesPool) || errors.Is(err, policy.ErrNoRoutableModels) {
					if !readmitted && readmitSessionStrikes(ctx, &request) {
						readmitted = true
						continue
					}
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
		// An attempt may outlive the rotation budget; its own error, not the
		// elapsed clock, decides whether another target may serve the turn.
		if !isSubscriptionPoolError(err) && !errors.Is(err, ErrCreditsExhaustedSubscriptionUnavailable) && !providers.IsRetryable(err) &&
			!codexSubscriptionModelRejected(err) && !anthropicSubscriptionModelRejected(err) && !codexOAuthCredentialRejected(err) && !anthropicOAuthCredentialRejected(err) && !anthropicSubscriptionThirdPartyRefused(err) {
			return winner, err
		}
		observability.FromContext(ctx).Info("Subscription model unavailable; selecting another included target", "model", target.Model, "provider", target.Provider)
		recordTurnFailure(ctx, target.Model)
		remainingModels := includedModels[:0]
		for _, model := range includedModels {
			if model != target.Model {
				remainingModels = append(remainingModels, model)
			}
		}
		includedModels = remainingModels
		if hasFixedTarget {
			break
		}
	}
	if billing.SubscriptionOnlyFromContext(ctx) && !linkedFirst(ctx) {
		return -1, subscriptionPoolExhausted(ctx)
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	paidModels := installationSubscriptionModelsWhenInactiveFromContext(ctx)
	paidCtx, paidReq := s.subscriptionStateRequest(ctx, request, paidModels)
	paidCtx = billing.ReleaseLinkedFirst(paidCtx)
	paidCtx = context.WithValue(paidCtx, subscriptionAPIOnlyKey{}, true)
	paidCtx = withSuppressedClaudeSubscription(withSuppressedCodexSubscription(paidCtx))
	target := in.initialDecision
	for {
		target = openRouterOverflowTarget(paidCtx, target)
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
			fallback, ok := s.openRouterOverflowFallbackTarget(paidCtx, paidReq, paidModels, in.initialDecision)
			if !ok {
				return -1, err
			}
			target = fallback
		}
	}
	observability.FromContext(ctx).Info("Included subscription targets unavailable; using exhausted model set", "model", target.Model, "provider", target.Provider)
	return s.dispatchSubscriptionStateTarget(paidCtx, in, target, false)
}

// includedSubscriptionModels lists every model a live subscription can serve,
// active-set models first. The active set is a preference, not a limit: a model
// outside it may still rescue a failed active model on included capacity.
func (s *Service) includedSubscriptionModels(ctx context.Context, req router.Request, headers http.Header) []string {
	preferred := installationSubscriptionModelsWhenActiveFromContext(ctx)
	candidates := append([]string{}, preferred...)
	for _, entry := range catalog.Models {
		if !slices.Contains(preferred, entry.ID) {
			candidates = append(candidates, entry.ID)
		}
	}
	models := make([]string, 0, len(candidates))
	for _, model := range candidates {
		if req.AllowedModels != nil && model != req.ForceModel {
			if _, permitted := req.AllowedModels[model]; !permitted {
				continue
			}
		}
		if s.subscriptionStateModelAvailable(ctx, req, headers, model) {
			models = append(models, model)
		}
	}
	return models
}

func (s *Service) subscriptionStatePaidTargetAvailable(ctx context.Context, headers http.Header, target router.Decision) bool {
	resolvedCredentials := s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, headers)
	if servedOnSubscription(resolvedCredentials) || servedOnCodexSubscription(resolvedCredentials) {
		return false
	}
	if s.subscriptionAPIAuthorization(s.withSubscriptionFundingAuthorization(ctx, headers), target.Provider, target.Model) != nil {
		return false
	}
	return len(s.subscriptionStateTargetBindings(resolvedCredentials, target)) > 0
}

func (s *Service) dispatchSubscriptionStateTarget(ctx context.Context, in failoverInputs, target router.Decision, usesIncludedSubscription bool) (int, error) {
	ctx = s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, in.subscriptionStateHeaders)
	resolvedTargetBindings := s.subscriptionStateTargetBindings(ctx, target)
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
	if in.onSubscriptionStateTarget != nil {
		in.onSubscriptionStateTarget(target, in.bindings)
	} else if in.onAlternative != nil {
		in.onAlternative(target)
	}
	winner, err := s.dispatchWithFallback(ctx, in)
	if err == nil && in.subscriptionStateWinnerProvider != nil && winner >= 0 && winner < len(resolvedTargetBindings) {
		*in.subscriptionStateWinnerProvider = resolvedTargetBindings[winner].Provider
	}
	return winner, err
}
