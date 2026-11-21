package proxy

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

type subscriptionStateAllowedModelsKey struct{}

func subscriptionStateModelsEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(InstallationSubscriptionStateModelsEnabledContextKey{}).(bool)
	return !subscriptionFundingOutOfPlayForRequest(ctx) &&
		(enabled || openRouterOverflowEnabled(ctx) ||
			len(installationSubscriptionModelsWhenActiveFromContext(ctx)) > 0 ||
			len(installationSubscriptionModelsWhenInactiveFromContext(ctx)) > 0)
}

func (s *Service) subscriptionStateRequest(ctx context.Context, req router.Request, models []string) (context.Context, router.Request) {
	req = candidates.ReadmitForcedContextEstimate(req, req.ForceModel)
	allowed := make(map[string]struct{})
	for _, model := range models {
		if _, excluded := req.Candidates.ExcludedModels[model]; excluded {
			continue
		}
		if _, excluded := req.Candidates.AutomaticExcludedModels[model]; excluded && model != req.ForceModel {
			continue
		}
		if _, excluded := req.Candidates.SafetyExcludedModels[model]; excluded {
			continue
		}
		if _, excluded := req.Candidates.UnsignedHistoryExcludedModels[model]; excluded {
			continue
		}
		if req.Candidates.AllowedModels != nil && model != req.ForceModel {
			if _, permitted := req.Candidates.AllowedModels[model]; !permitted {
				continue
			}
		}
		allowed[model] = struct{}{}
	}
	ctx = context.WithValue(ctx, subscriptionStateAllowedModelsKey{}, allowed)
	req.Candidates.AllowedModels = allowed
	req.Candidates.ExcludedModels = candidates.MergeExcludedModels(req.Candidates.ExcludedModels, s.excludedModelsForRequest(ctx))
	return ctx, req
}

func (s *Service) memberPickPaidContext(ctx context.Context, req router.Request, headers http.Header, pick router.Decision, includedModels []string) (context.Context, bool) {
	if slices.Contains(includedModels, pick.Model) || !memberSelectedModel(ctx, pick.Model) {
		return ctx, false
	}
	if !slices.Contains(installationSubscriptionModelsWhenInactiveFromContext(ctx), pick.Model) {
		paidModels, _ := ctx.Value(InstallationSubscriptionModelsWhenInactiveContextKey{}).([]string)
		ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, append(slices.Clone(paidModels), pick.Model))
	}
	paidModels := installationSubscriptionModelsWhenInactiveFromContext(ctx)
	if !slices.Contains(paidModels, pick.Model) {
		return ctx, false
	}
	paidCtx, paidReq := s.subscriptionStateRequest(ctx, req, paidModels)
	if _, admitted := paidReq.Candidates.AllowedModels[pick.Model]; !admitted {
		return ctx, false
	}
	paidCtx = withIncludedLaneExhausted(paidCtx)
	return ctx, s.subscriptionStatePaidTargetRefusal(paidCtx, headers, paidReq, openRouterOverflowTarget(paidCtx, pick)) == nil
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
		if seatAdmissionOf(s).mayAdmit(ctx, binding.Provider, model) {
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

func readmitSessionStrikes(ctx context.Context, request *router.Request) bool {
	readmit := sessionStrikeReadmitModelsFromContext(ctx)
	if len(readmit) == 0 {
		return false
	}
	excluded := maps.Clone(request.Candidates.AutomaticExcludedModels)
	for _, model := range readmit {
		delete(excluded, model)
	}
	request.Candidates.AutomaticExcludedModels = excluded
	return true
}

func beginSubscriptionStateLane(ctx context.Context, initial router.Decision) context.Context {
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil && usage.IntendedModel == "" {
		usage.IntendedModel = initial.Model
	}
	return withClientAppRefusalPaidFallback(ctx, initial)
}

func (s *Service) dispatchSubscriptionStateModels(ctx context.Context, in dispatch.FailoverInputs) (int, error) {
	request := *in.SubscriptionStateRequest
	ctx = beginSubscriptionStateLane(ctx, in.InitialDecision)
	sessionUnavailableModels := modelSet(sessionDemotedModelsFromContext(ctx))
	if sessionUnavailableModels == nil {
		sessionUnavailableModels = make(map[string]struct{})
	}
	for model, cooldownUntil := range sessionCooldownModelsFromContext(ctx) {
		if cooldownUntil.After(s.clock.Now()) {
			sessionUnavailableModels[model] = struct{}{}
		}
	}
	request.Candidates.AutomaticExcludedModels = candidates.MergeExcludedModels(request.Candidates.AutomaticExcludedModels, sessionUnavailableModels)
	if in.InitialDecision.UserForcedModel() {
		request.ForceModel = in.InitialDecision.Model
	}
	in.SubscriptionStateRequest = nil
	in.Alternatives = nil
	includedModels := s.includedSubscriptionModels(ctx, request, in.SubscriptionStateHeaders)
	if pickCtx, served := s.memberPickPaidContext(ctx, request, in.SubscriptionStateHeaders, in.InitialDecision, includedModels); served {
		ctx = pickCtx
		includedModels = nil
	}
	ctx, rotationBudget := withSubscriptionAdmissionBudget(ctx)
	hasFixedTarget := request.ForceModel != "" || callerModelPassthroughActive(ctx) ||
		in.Origin == policy.OverrideSourceDeployment || in.Origin == policy.OverrideSourceRequest
	readmitted := false
	var includedFailure error
	var throttleWaited time.Duration
	var spentCodexModels map[string]struct{}
	for len(includedModels) > 0 && rotationBudget.remaining > 0 {
		attemptModels := includedModels
		if !hasFixedTarget {
			attemptModels = s.withoutHeldSubscriptionModels(ctx, includedModels)
		}
		attemptCtx, attemptReq := s.subscriptionStateRequest(ctx, request, attemptModels)
		if len(attemptReq.Candidates.AllowedModels) == 0 && len(attemptModels) < len(includedModels) {
			attemptCtx, attemptReq = s.subscriptionStateRequest(ctx, request, includedModels)
		}
		attemptCtx = withSubscriptionAttempt(attemptCtx)
		attemptReq.Candidates.ExcludedModels = candidates.MergeExcludedModels(attemptReq.Candidates.ExcludedModels, spentCodexModels)
		attemptReq.EnabledProviders = make(map[string]struct{})
		excludedProviders := s.excludedProvidersForRequest(ctx)
		for provider := range request.EnabledProviders {
			_, excluded := excludedProviders[provider]
			if s.supportsSubscriptionTransport(provider) && !excluded {
				attemptReq.EnabledProviders[provider] = struct{}{}
			}
		}
		target := in.InitialDecision
		if !hasFixedTarget {
			if provider, engaged := s.usageBypassEngaged(ctx, in.SubscriptionStateHeaders, attemptReq); engaged {
				target.Model = request.RequestedModel
				target.Provider = provider
				target.Reason = reasonUsageBypass
				target.Metadata = nil
			}
		}
		_, permitted := attemptReq.Candidates.AllowedModels[target.Model]
		permitted = permitted && s.supportsSubscriptionTransport(target.Provider)
		if !permitted && !hasFixedTarget {
			var err error
			target, err = s.Route(attemptCtx, attemptReq)
			if err != nil {
				if paidLaneEmpty(err) {
					if !readmitted && readmitSessionStrikes(ctx, &request) {
						readmitted = true
						continue
					}
					break
				}
				return -1, err
			}
		}
		if _, permitted := attemptReq.Candidates.AllowedModels[target.Model]; !permitted || !s.supportsSubscriptionTransport(target.Provider) {
			break
		}
		winner, err := s.dispatchSubscriptionStateTarget(attemptCtx, in, target, true)
		if err == nil || committed(in.Prelude) || ctx.Err() != nil {
			return winner, err
		}
		if !isSubscriptionPoolError(err) && !subscriptionLaneRefused(err) && !providers.IsRetryable(err) &&
			!codexSubscriptionModelRejected(err) && !anthropicSubscriptionModelRejected(err) && !codexOAuthCredentialRejected(err) && !anthropicOAuthCredentialRejected(err) && !anthropicClientAppRefused(ctx, err) {
			return winner, err
		}
		if !anotherSeatPoolCanServe(ctx, seatAdmissionOf(s), includedModels, target) && waitOutShortSeatThrottle(ctx, target.Provider, target.Model, err, s.clock.Now(), s.retrySleep, &throttleWaited) {
			continue
		}
		includedFailure = includedFailureAfter(includedFailure, err, ownCodexLoginRejected(ctx, in.SubscriptionStateHeaders, s.usageObserver))
		observability.FromContext(ctx).Info("Subscription model unavailable; selecting another included target", "model", target.Model, "provider", target.Provider)
		strikeModelUnlessFundingFailure(ctx, target.Model, err)
		if !hasFixedTarget {
			s.holdSessionOffSubscriptionPool(ctx, target.Provider, target.Model)
		}
		includedModels, spentCodexModels = dropFailedIncludedModels(includedModels, target.Model, spentCodexModels, codexPlanSpentForTurn(ctx, in.SubscriptionStateHeaders, s.usageObserver, target.Provider, err, s.codexSubscriptionExhausted(ctx, in.SubscriptionStateHeaders)))
		if hasFixedTarget {
			break
		}
	}
	if err := includedLaneVerdict(s.sessionHolds.withUnservedHoldsReleased(ctx, request.EnabledProviders)); err != nil {
		return -1, flushIncludedLaneVerdict(in, err)
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	paidModels := installationSubscriptionModelsWhenInactiveFromContext(ctx)
	paidCtx, paidReq := s.subscriptionStateRequest(ctx, request, paidModels)
	paidCtx = withIncludedLaneExhausted(paidCtx)
	target := in.InitialDecision
	for {
		target = openRouterOverflowTarget(paidCtx, target)
		refusal := s.subscriptionStatePaidTargetRefusal(paidCtx, in.SubscriptionStateHeaders, paidReq, target)
		if refusal == nil {
			break
		}
		if hasFixedTarget {
			return -1, attributeCodexLoginRejection(cmp.Or(includedFailure, refusal))
		}
		if _, permitted := paidReq.Candidates.AllowedModels[target.Model]; permitted {
			request.Candidates.ExcludedModels = candidates.MergeExcludedModels(request.Candidates.ExcludedModels, map[string]struct{}{target.Model: {}})
			paidCtx, paidReq = s.subscriptionStateRequest(paidCtx, request, paidModels)
		}
		if len(paidReq.Candidates.AllowedModels) == 0 {
			return -1, emptyPaidPoolErr(ctx, includedFailure, s.clock.Now())
		}
		var err error
		target, err = s.Route(paidCtx, paidReq)
		if err != nil {
			fallback, ok := s.openRouterOverflowFallbackTarget(paidCtx, paidReq, paidModels, in.InitialDecision)
			if !ok {
				return -1, includedFailureOr(includedFailure, err)
			}
			target = fallback
		}
	}
	observability.FromContext(ctx).Info("Included subscription targets unavailable; using exhausted model set", "model", target.Model, "provider", target.Provider)
	return s.dispatchSubscriptionStateTarget(paidCtx, in, target, false)
}

func includedFailureOr(includedFailure, paidErr error) error {
	if includedFailure == nil || !paidLaneEmpty(paidErr) {
		return paidErr
	}
	return attributeCodexLoginRejection(includedFailure)
}

func paidLaneEmpty(err error) bool {
	return errors.Is(err, policy.ErrNoEligibleArm) || errors.Is(err, router.ErrAllowlistEmptiesPool) || errors.Is(err, policy.ErrNoRoutableModels)
}

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
		if req.Candidates.AllowedModels != nil && model != req.ForceModel {
			if _, permitted := req.Candidates.AllowedModels[model]; !permitted {
				continue
			}
		}
		if s.subscriptionStateModelAvailable(ctx, req, headers, model) {
			models = append(models, model)
		}
	}
	return models
}

func (s *Service) subscriptionStatePaidTargetRefusal(ctx context.Context, headers http.Header, paidReq router.Request, target router.Decision) error {
	if _, permitted := paidReq.Candidates.AllowedModels[target.Model]; !permitted {
		return router.ErrAllowlistEmptiesPool
	}
	resolvedCredentials := s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, headers)
	if servedOnSubscription(resolvedCredentials) || servedOnCodexSubscription(resolvedCredentials) {
		return router.ErrAllowlistEmptiesPool
	}
	if err := s.subscriptionAPIAuthorization(s.withSubscriptionFundingAuthorization(ctx, headers), target.Provider, target.Model); err != nil {
		return err
	}
	if len(s.subscriptionStateTargetBindings(resolvedCredentials, target)) == 0 {
		return router.ErrAllowlistEmptiesPool
	}
	return nil
}

func (s *Service) dispatchSubscriptionStateTarget(ctx context.Context, in dispatch.FailoverInputs, target router.Decision, usesIncludedSubscription bool) (int, error) {
	ctx = s.resolveCredentials(clearCredentials(ctx), target.Provider, target.Model, in.SubscriptionStateHeaders)
	resolvedTargetBindings := s.subscriptionStateTargetBindings(ctx, target)
	if !usesIncludedSubscription && in.OnSubscriptionStatePaidTarget != nil {
		in.OnSubscriptionStatePaidTarget(target, resolvedTargetBindings)
	}
	attempt, err := in.BuildAlternative(target)
	if err != nil {
		return -1, err
	}
	if in.Prelude != nil {
		in.Prelude.Discard()
	}
	in.InitialDecision = target
	in.Attempt = attempt
	in.Bindings = resolvedTargetBindings
	if usesIncludedSubscription {
		in.Bindings = []catalog.ProviderBinding{{Provider: target.Provider}}
		in.DeferFlushOnExhaustion = true
	}
	if in.OnSubscriptionStateTarget != nil {
		in.OnSubscriptionStateTarget(target, in.Bindings)
	} else if in.OnAlternative != nil {
		in.OnAlternative(target)
	}
	winner, err := s.dispatchWithFallback(ctx, in)
	if err == nil && in.SubscriptionStateWinnerProvider != nil && winner >= 0 && winner < len(resolvedTargetBindings) {
		*in.SubscriptionStateWinnerProvider = resolvedTargetBindings[winner].Provider
	}
	return winner, err
}
