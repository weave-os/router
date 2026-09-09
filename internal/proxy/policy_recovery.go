package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/observability/apm"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"
)

type strictPolicyKey struct{}

func withServingPolicyContext(ctx context.Context, r *http.Request) context.Context {
	ctx = policy.WithDecisionBudget(ctx)
	if hasEvalOverrideHeader(r) {
		ctx = context.WithValue(ctx, strictPolicyKey{}, true)
	}
	return ctx
}

const policyRecoveryReason = "policy_recovery"

func recordClassificationEvidence(ctx context.Context, sourceMessages []router.ConversationMessage, dispatchedCount int) {
	textBytes := 0
	for _, message := range sourceMessages {
		textBytes += len(message.Text)
	}
	observability.FromContext(ctx).Debug("Classification evidence retained",
		"source_message_count", len(sourceMessages), "source_text_bytes", textBytes,
		"dispatch_message_count", dispatchedCount, "user_boundary_present", router.UserTextIndex(sourceMessages) >= 0)
}

func resolveRecoveryCredentials(ctx context.Context, decision router.Decision, headers http.Header) context.Context {
	if decision.Recovery != nil {
		ctx = requestcontext.WithCredentials(ctx, nil)
	}
	return resolveAndInjectCredentials(ctx, decision.Provider, decision.Model, headers)
}

func recoveryRetryable(err error) bool {
	return providers.IsRetryable(err) || providers.IsUpstreamModelNotFound(err) || providers.IsUpstreamProviderBillingBlocked(err)
}

func (s *Service) recoverPolicy(ctx context.Context, req router.Request, failure error) (router.Decision, error) {
	if err := ctx.Err(); err != nil {
		return router.Decision{}, err
	}
	_, shadow := AgentShadowEvalFromContext(ctx)
	strict, _ := ctx.Value(strictPolicyKey{}).(bool)
	if !s.policyDeadlineFallback || !errors.Is(failure, hmm.ErrHMMUnavailable) || req.ShadowMode || shadow || strict || req.ForceModel != "" || req.ForceCluster != "" {
		return router.Decision{}, failure
	}
	plans, err := s.inferencePlans()
	if err != nil {
		apm.RecordPolicyRecovery(ctx, policy.FailureReasonFor(failure), apm.RecoveryUnavailable)
		observability.FromContext(ctx).Error("Policy recovery plan resolver failed", "policy_failure", policy.FailureReasonFor(failure), "err", err)
		return router.Decision{}, errors.Join(failure, err)
	}
	previousModel := req.RecoveryPreviousModel
	purpose := policy.PurposeAnthropicMessages
	switch req.TranslationRequirements.Endpoint {
	case router.EndpointOpenAIChat:
		purpose = policy.PurposeOpenAIChatCompletions
	case router.EndpointOpenAIResponses:
		purpose = policy.PurposeOpenAIResponses
	case router.EndpointGeminiGenerate:
		purpose = policy.PurposeGeminiGenerateContent
	}
	req.ExcludedModels = mergeExcludedModels(req.ExcludedModels, req.SafetyExcludedModels)
	if req.EnabledProviders == nil {
		req.EnabledProviders = s.clients.NameSet()
	}
	var fitsBinding func(policy.Binding) bool
	if req.DispatchContext != nil {
		fitsBinding = func(binding policy.Binding) bool {
			return siblingFitsContext(binding.CatalogID, binding.Provider, req.DispatchContext.InputTokens, req.DispatchContext.SignatureSavings, req.DispatchContext.OutputReserve)
		}
	}
	authorizedPlans, err := plans.ResolveRecovery(policy.ResolutionRequest{Purpose: purpose, RouterRequest: req}, previousModel, s.policyDeadlineDefaultModel, fitsBinding)
	if err != nil {
		apm.RecordPolicyRecovery(ctx, policy.FailureReasonFor(failure), apm.RecoveryUnavailable)
		observability.FromContext(ctx).Error("Policy recovery has no authorized target", "policy_failure", policy.FailureReasonFor(failure), "err", err)
		return router.Decision{}, errors.Join(failure, err)
	}
	recovery := &router.ServingRecovery{Plans: authorizedPlans, Budget: &inference.AttemptBudget{Remaining: policy.RecoveryMaxAttempts, Deadline: time.Now().Add(policy.RecoveryRetryWindow)}, Failure: failure}
	decision := recoveryDecision(recovery)
	apm.RecordPolicyRecovery(ctx, policy.FailureReasonFor(failure), apm.RecoveryAuthorized)
	observability.FromContext(ctx).Warn("Policy classification failed; serving authorized recovery", "policy_failure", policy.FailureReasonFor(failure), "err", failure, "model", decision.Model, "provider", decision.Provider, "recovery_candidates", len(authorizedPlans), "policy_revision", authorizedPlans[0].PolicyRevision())
	return decision, nil
}

func recoveryDecision(recovery *router.ServingRecovery) router.Decision {
	target := recovery.Plans[0].SelectedTarget()
	return router.Decision{Provider: target.Provider, Model: target.CatalogID, Effort: target.Effort, Reason: policyRecoveryReason, Recovery: recovery}
}

func nextRecoveryDecision(failed router.Decision) (router.Decision, bool) {
	if failed.Recovery == nil || len(failed.Recovery.Plans) < 2 {
		return router.Decision{}, false
	}
	recovery := *failed.Recovery
	recovery.Plans = recovery.Plans[1:]
	return recoveryDecision(&recovery), true
}

func recoveryPlan(decision router.Decision, purpose inference.Purpose) (inference.ResolvedPlan, error) {
	plan := decision.Recovery.Plans[0]
	target := plan.SelectedTarget()
	if target.CatalogID != decision.Model || target.Provider != decision.Provider || plan.Purpose() != purpose {
		return nil, fmt.Errorf("policy recovery target or purpose changed after authorization")
	}
	return plan, nil
}

func recordRecoveryOutcome(ctx context.Context, decision router.Decision, err error) {
	if decision.Recovery == nil {
		return
	}
	outcome := apm.RecoveryServed
	if err != nil {
		outcome = apm.RecoveryFailed
	}
	apm.RecordPolicyRecovery(ctx, policy.FailureReasonFor(decision.Recovery.Failure), outcome)
}
