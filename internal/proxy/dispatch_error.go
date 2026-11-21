package proxy

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"
)

type DispatchErrorKind int

const (
	DispatchErrorNone DispatchErrorKind = iota
	DispatchErrorUpstreamStatus
	DispatchErrorNotImplemented
	DispatchErrorProviderNotConfigured
	DispatchErrorAnthropicFederationUnavailable
	DispatchErrorRequestNotJSONObject
	DispatchErrorResponsesChatCompletionsBody
	DispatchErrorNoEligibleProvider
	DispatchErrorAllowlistEmptiesPool
	DispatchErrorContextWindowExceeded
	DispatchErrorImageLimitExceeded
	DispatchErrorInvalidRoutingKnobs
	DispatchErrorRLPolicyUnavailable
	DispatchErrorHMMUnavailable
	DispatchErrorPolicyUnavailable
	DispatchErrorClusterUnavailable
	DispatchErrorCreditsExhausted
	DispatchErrorSubscriptionPoolExhausted
	DispatchErrorSubscriptionPoolUnavailable
	DispatchErrorSubscriptionPoolThrottled
	DispatchErrorTranslationIntrinsicallyIncompatible
	DispatchErrorTranslationProviderUnavailable
	DispatchErrorUserSpendLimitReached
	DispatchErrorSpendLimitUnavailable
	DispatchErrorAnthropicCacheControlInvalid
	DispatchErrorForcedModelExcluded
	DispatchErrorForcedModelUnknown
	DispatchErrorPassthroughModelUnknown
	DispatchErrorForcedClusterUnsupportedStrategy
	DispatchErrorForcedClusterUnservable
	DispatchErrorRoutedModelIncompatible
	DispatchErrorGatewayServesNoModel
	DispatchErrorNoRoutableModels
	DispatchErrorPlanTargetMismatch
	DispatchErrorPlanOverrideRejected
	DispatchErrorPlanUnresolvable
	DispatchErrorPolicyPinUnavailable
	DispatchErrorHandoff
	DispatchErrorProductIneligible
	DispatchErrorClassifierHistory
	DispatchErrorClassifierInputTooLong
	DispatchErrorClassifierUnavailable
	DispatchErrorSpendCapReached
	DispatchErrorSubscriptionClientAppRefused
	DispatchErrorDecisionModelUnavailable
	DispatchErrorCompactionUnservable
	DispatchErrorClaudeEnterpriseOnly
	DispatchErrorWeaveCreditsDisabled
	DispatchErrorSubscriptionUnavailableForTurn
	DispatchErrorNoEligibleArm
)

type DispatchErrorClass struct {
	Kind          DispatchErrorKind
	Status        int
	Message       string
	RetryAfter    bool
	NoClientRetry bool
	LogLevel      string
	LogMessage    string
	Refusal       *refusal.Contract
}

func ClassifyDispatchError(err error) (DispatchErrorClass, bool) {
	var statusErr *providers.UpstreamStatusError
	var bufferedErr *providers.UpstreamErrorResponse
	var forcedExcluded *ForcedModelExcludedError
	var forcedUnknown *ForcedModelUnknownError
	var passthroughUnknown *PassthroughModelUnknownError
	var forcedClusterStrategy *ForcedClusterUnsupportedStrategyError
	var forcedClusterUnservable *policy.ForcedClusterUnservableError
	var resolution *policy.ResolutionError
	var compactionUnservable *CompactionUnservableError
	switch {
	case errors.Is(err, auth.ErrAnthropicFederationUnavailable):
		return DispatchErrorClass{Kind: DispatchErrorAnthropicFederationUnavailable, Status: http.StatusServiceUnavailable, Message: "Anthropic workload federation is unavailable.", RetryAfter: true}, true
	case errors.Is(err, policy.ErrClassifierWarming):
		return DispatchErrorClass{Kind: DispatchErrorPolicyUnavailable, Status: http.StatusServiceUnavailable,
			Message: policy.ClassifierWarmingMessage, RetryAfter: true, LogLevel: "warn", LogMessage: "Classifier warming"}, true
	case errors.Is(err, router.ErrClassifierHistoryUnavailable), errors.Is(err, router.ErrClassifierThreadInvalid):
		return DispatchErrorClass{Kind: DispatchErrorClassifierHistory, Status: http.StatusConflict, Message: "Classifier thread history is unavailable or changed. Restore the complete original history; do not re-enroll a compacted thread.", LogLevel: "warn", LogMessage: "Classifier history rejected"}, true
	case errors.Is(err, router.ErrClassifierInputTooLong):
		return DispatchErrorClass{Kind: DispatchErrorClassifierInputTooLong, Status: http.StatusRequestEntityTooLarge, Message: "Classifier input exceeds its inference limit. No history was truncated and no alternate policy was used.", LogLevel: "warn", LogMessage: "Classifier input exceeds inference limit"}, true
	case errors.Is(err, router.ErrClassifierUnavailable):
		return DispatchErrorClass{Kind: DispatchErrorClassifierUnavailable, Status: http.StatusServiceUnavailable, Message: "The thread's classifier release is unavailable. No alternate policy was used.", LogLevel: "warn", LogMessage: "Classifier release unavailable"}, true
	case errors.Is(err, eligibility.ErrModelIneligible):
		return refusalClass(DispatchErrorProductIneligible, refusal.ProductIneligible,
			"This model is not included in your plan. Pick a model your plan covers.",
			"warn", "Rejected request: model is not eligible for the caller's product"), true
	case errors.Is(err, ErrSubscriptionClientAppRefused):
		return refusalClass(DispatchErrorSubscriptionClientAppRefused, refusal.SubscriptionClientAppRefused,
			"Anthropic only lets Claude Code use Claude subscription plan limits, so this app can't use them for the forced Claude model. Use Claude Code, add extra usage at claude.ai/settings/usage, or pick a non-Claude model.",
			"warn", "Rejected forced Claude turn: subscriptions refused third-party client"), true
	case subscriptionLaneRefused(err):
		return subscriptionLaneRefusalClass(err), true
	case errors.Is(err, ErrWeaveCreditsDisabled):
		return refusalClass(DispatchErrorWeaveCreditsDisabled, refusal.WeaveCreditsDisabled,
			"Weave credits are turned off for your organization, and none of your plans or provider API keys can serve this request. Connect a provider key or plan for this model, or allow Weave credits in settings.",
			"warn", "Request refused: Weave credits disabled and no customer funding source"), true
	case errors.Is(err, ErrClaudeEnterpriseSubscriptionUnavailable):
		return refusalClass(DispatchErrorClaudeEnterpriseOnly, refusal.ClaudeEnterpriseOnly,
			"Claude Enterprise-only billing is enabled, but no usable Claude account can serve this request. Connect or reconnect a Claude account, or turn off Enterprise-only billing in settings.",
			"warn", "Claude Enterprise-only request refused"), true
	case errors.Is(err, ErrSubscriptionPoolExhausted):
		return refusalClass(DispatchErrorSubscriptionPoolExhausted, refusal.SubscriptionPoolExhausted,
			"All enrolled subscription accounts are currently unavailable.",
			"warn", "Subscription account pool exhausted"), true
	case errors.Is(err, ErrSubscriptionPoolThrottled):
		return refusalClass(DispatchErrorSubscriptionPoolThrottled, refusal.SubscriptionPoolThrottled,
			subscriptionPoolThrottledMessage,
			"warn", "Subscription account pool throttled"), true
	case errors.Is(err, ErrSubscriptionPoolUnavailable):
		return refusalClass(DispatchErrorSubscriptionPoolUnavailable, refusal.SubscriptionPoolUnavailable,
			"Subscription account service is temporarily unavailable.",
			"error", "Subscription account pool unavailable"), true
	case errors.Is(err, ErrHandoffInvalid):
		return DispatchErrorClass{Kind: DispatchErrorHandoff, Status: http.StatusConflict, Message: handoffInvalidMessage, LogLevel: "warn", LogMessage: "Pi handoff rejected"}, true
	case errors.Is(err, ErrHandoffUnavailable):
		return DispatchErrorClass{Kind: DispatchErrorHandoff, Status: http.StatusServiceUnavailable, Message: handoffUnavailableMessage, LogLevel: "warn", LogMessage: "Pi handoff unavailable"}, true
	case errors.As(err, &forcedExcluded):
		return DispatchErrorClass{
			Kind:       DispatchErrorForcedModelExcluded,
			Status:     http.StatusBadRequest,
			Message:    forcedExcluded.Reason + ". Clear it with the unforce-model directive, or pick a model from a permitted provider.",
			LogLevel:   "warn",
			LogMessage: "Rejected request: forced model is excluded on this installation",
		}, true
	case errors.As(err, &forcedUnknown):
		return DispatchErrorClass{
			Kind:       DispatchErrorForcedModelUnknown,
			Status:     http.StatusBadRequest,
			Message:    forcedUnknown.Error() + ". Use a full model ID, e.g. claude-opus-5, gpt-5.6-sol, or gemini-3-pro-preview.",
			LogLevel:   "warn",
			LogMessage: "Rejected request: forced model is not a known model",
		}, true
	case errors.As(err, &passthroughUnknown):
		message := passthroughUnknown.Error() + ". Request a full model ID (e.g. claude-opus-5-5 or gpt-5.6-sol)."
		if passthroughUnknown.RoutingPolicyPassthrough {
			message = "Automatic routing isn't enabled for you in this organization, so the requested model is served as-is, but " + passthroughUnknown.Error() + ". Request a full model ID (e.g. claude-opus-5-5 or gpt-5.6-sol), or ask your org admin to enable automatic routing."
		}
		return DispatchErrorClass{
			Kind:       DispatchErrorPassthroughModelUnknown,
			Status:     http.StatusBadRequest,
			Message:    message,
			LogLevel:   "warn",
			LogMessage: "Rejected request: passthrough requested model is not a known model",
		}, true
	case errors.As(err, &forcedClusterStrategy):
		return DispatchErrorClass{
			Kind:       DispatchErrorForcedClusterUnsupportedStrategy,
			Status:     http.StatusBadRequest,
			Message:    forcedClusterStrategy.Error() + ". Clear " + ForceClusterHeader + ", or move the installation to a policy-sidecar strategy.",
			LogLevel:   "warn",
			LogMessage: "Rejected request: forced cluster on a strategy with no policy sidecar",
		}, true
	case errors.As(err, &forcedClusterUnservable):
		return DispatchErrorClass{
			Kind:       DispatchErrorForcedClusterUnservable,
			Status:     http.StatusBadRequest,
			Message:    forcedClusterUnservable.Reason + ". Clear " + ForceClusterHeader + " or update the cluster's model list.",
			LogLevel:   "warn",
			LogMessage: "Rejected request: forced cluster has no eligible model",
		}, true
	case errors.As(err, &compactionUnservable):
		return compactionUnservableClassFor(compactionUnservable), true
	case isContextOverflow(err):
		return contextWindowExceededClass, true
	case errors.As(err, &statusErr):
		return DispatchErrorClass{
			Kind:    DispatchErrorUpstreamStatus,
			Status:  statusErr.Status,
			Message: TerminalErrorMessage(err),
		}, true
	case errors.As(err, &bufferedErr):
		return DispatchErrorClass{
			Kind:    DispatchErrorUpstreamStatus,
			Status:  bufferedErr.Status,
			Message: TerminalErrorMessage(err),
		}, true
	case errors.Is(err, providers.ErrNotImplemented):
		return DispatchErrorClass{
			Kind:    DispatchErrorNotImplemented,
			Status:  http.StatusNotImplemented,
			Message: "Provider not implemented.",
		}, true
	case errors.Is(err, ErrProviderNotConfigured), errors.Is(err, dispatch.ErrProviderNotConfigured):
		return DispatchErrorClass{
			Kind:    DispatchErrorProviderNotConfigured,
			Status:  http.StatusBadGateway,
			Message: "Provider not configured.",
		}, true
	case errors.Is(err, ErrRequestNotJSONObject):
		return DispatchErrorClass{
			Kind:    DispatchErrorRequestNotJSONObject,
			Status:  http.StatusBadRequest,
			Message: "Request body must be a JSON object.",
		}, true
	case errors.Is(err, translate.ErrResponsesChatCompletionsBody):
		return DispatchErrorClass{
			Kind:       DispatchErrorResponsesChatCompletionsBody,
			Status:     http.StatusBadRequest,
			Message:    "Unsupported parameter: 'messages'. In the Responses API, this parameter has moved to 'input'. Try again with the new parameter, or POST this body to /v1/chat/completions.",
			LogLevel:   "warn",
			LogMessage: "Rejected request: Chat Completions body posted to /v1/responses",
		}, true
	case errors.Is(err, translate.ErrAnthropicCacheControlOverflow), errors.Is(err, translate.ErrAnthropicCacheControlInvalid):
		return DispatchErrorClass{
			Kind:       DispatchErrorAnthropicCacheControlInvalid,
			Status:     http.StatusBadRequest,
			Message:    unwrapToSentinelMessage(err),
			LogLevel:   "warn",
			LogMessage: "Rejected request: invalid Anthropic cache_control",
		}, true
	case errors.Is(err, ErrTranslationIntrinsicallyIncompatible):
		return DispatchErrorClass{
			Kind:       DispatchErrorTranslationIntrinsicallyIncompatible,
			Status:     http.StatusBadRequest,
			Message:    "This request requires a native compatibility path that the router does not support.",
			LogLevel:   "warn",
			LogMessage: "Translation requirements are intrinsically incompatible",
		}, true
	case errors.Is(err, ErrTranslationCompatibleProviderUnavailable):
		return DispatchErrorClass{
			Kind:       DispatchErrorTranslationProviderUnavailable,
			Status:     http.StatusServiceUnavailable,
			Message:    "No compatible provider is currently available for this request.",
			RetryAfter: true,
			LogLevel:   "warn",
			LogMessage: "Compatible translation provider unavailable",
		}, true
	case translate.IsIntrinsicallyIncompatible(err):
		return DispatchErrorClass{
			Kind:       DispatchErrorRoutedModelIncompatible,
			Status:     http.StatusBadGateway,
			Message:    "The routed model cannot serve this request's shape (tool schema, reasoning intent, or tool history). Please retry.",
			LogLevel:   "warn",
			LogMessage: "Routed model cannot serve this request shape; no failover was available",
		}, true
	case errors.Is(err, router.ErrAllowlistEmptiesPool):
		return DispatchErrorClass{
			Kind:       DispatchErrorAllowlistEmptiesPool,
			Status:     http.StatusBadRequest,
			Message:    "No allowed model can serve this request: your organization's model allowlist has no eligible overlap. Ask your org admin to widen it.",
			LogLevel:   "warn",
			LogMessage: "Model allowlist leaves no eligible candidates",
		}, true
	case errors.Is(err, router.ErrNoEligibleProvider):
		return noEligibleProviderClass(), true
	case errors.Is(err, billing.ErrUserMonthlySpendLimitReached):
		return refusalClass(DispatchErrorUserSpendLimitReached, refusal.UserSpendLimitReached,
			"You've reached your monthly Weave Router spend limit. Ask your org admin to raise it, or it resets next month.",
			"warn", "Request refused: engineer monthly spend limit reached"), true
	case errors.Is(err, billing.ErrSpendLimitCheckUnavailable):
		return refusalClass(DispatchErrorSpendLimitUnavailable, refusal.SpendLimitUnavailable,
			"Billing system is temporarily unavailable. Retry in a few moments.",
			"error", "Spend-limit check unavailable"), true
	case errors.Is(err, router.ErrInvalidRoutingKnobs):
		return DispatchErrorClass{
			Kind:       DispatchErrorInvalidRoutingKnobs,
			Status:     http.StatusBadRequest,
			Message:    "Invalid routing knobs supplied.",
			LogLevel:   "warn",
			LogMessage: "Invalid routing knobs supplied",
		}, true
	case errors.Is(err, router.ErrPolicyPinUnavailable):
		return refusalClass(DispatchErrorPolicyPinUnavailable, refusal.PolicyPinUnavailable,
			router.PolicyPinUnavailableReason+": the pinned policy artifact or roster is not loaded on this router.",
			"warn", "Policy pin unavailable"), true
	case errors.Is(err, policy.ErrGatewayServesNoDeployedModel):
		return DispatchErrorClass{
			Kind:       DispatchErrorGatewayServesNoModel,
			Status:     http.StatusBadRequest,
			Message:    "No model can be routed through your gateway: your gateway keys don't alias any available model. Add model aliases on the key mapping each model you want routed to the name your gateway serves it under.",
			LogLevel:   "warn",
			LogMessage: "Gateway-exclusive routing has no aliased model",
		}, true
	case errors.Is(err, policy.ErrImageLimitExceeded):
		return DispatchErrorClass{
			Kind:       DispatchErrorImageLimitExceeded,
			Status:     http.StatusBadRequest,
			Message:    "This request carries more images than any model available to it accepts in one request. Send fewer images (or start a new conversation without earlier screenshots).",
			LogLevel:   "warn",
			LogMessage: "Request image count exceeds every eligible model's limit",
		}, true
	case errors.Is(err, policy.ErrNoProviderCredentials):
		return noEligibleProviderClass(), true
	case errors.Is(err, policy.ErrNoRoutableModels), errors.Is(err, policy.ErrNoEligibleArm):
		return routingEligibilityClass(err), true
	case errors.Is(err, dispatch.ErrTargetMismatch):
		return DispatchErrorClass{
			Kind:       DispatchErrorPlanTargetMismatch,
			Status:     http.StatusBadGateway,
			Message:    "Upstream call failed: the prepared request did not match the authorized inference target.",
			LogLevel:   "error",
			LogMessage: "Prepared request rejected by inference target check",
		}, true
	case errors.As(err, &resolution):
		return classifyResolutionError(resolution), true
	default:
		return classifyPolicyDispatchError(err)
	}
}

func unwrapToSentinelMessage(err error) string {
	for e := err; e != nil; e = errors.Unwrap(e) {
		child := errors.Unwrap(e)
		if errors.Unwrap(child) != nil {
			continue
		}
		if errors.Is(child, translate.ErrAnthropicCacheControlOverflow) || errors.Is(child, translate.ErrAnthropicCacheControlInvalid) {
			return translate.CacheControlClientMessage(e)
		}
	}
	return err.Error()
}

func (k DispatchErrorKind) IsClientError() bool {
	switch k {
	case DispatchErrorClassifierHistory, DispatchErrorClassifierInputTooLong:
		return true
	case DispatchErrorRequestNotJSONObject, DispatchErrorResponsesChatCompletionsBody, DispatchErrorNoEligibleProvider, DispatchErrorAllowlistEmptiesPool, DispatchErrorContextWindowExceeded, DispatchErrorImageLimitExceeded, DispatchErrorInvalidRoutingKnobs, DispatchErrorTranslationIntrinsicallyIncompatible, DispatchErrorAnthropicCacheControlInvalid, DispatchErrorForcedModelExcluded, DispatchErrorForcedModelUnknown, DispatchErrorPassthroughModelUnknown, DispatchErrorForcedClusterUnsupportedStrategy, DispatchErrorForcedClusterUnservable, DispatchErrorGatewayServesNoModel, DispatchErrorNoRoutableModels, DispatchErrorPlanOverrideRejected, DispatchErrorProductIneligible, DispatchErrorSubscriptionClientAppRefused, DispatchErrorNoEligibleArm:
		return true
	default:
		return false
	}
}

func LogDispatchErrorClass(log *slog.Logger, cls DispatchErrorClass, err error) {
	switch cls.LogLevel {
	case "warn":
		log.Warn(cls.LogMessage, "err", err)
	case "error":
		log.Error(cls.LogMessage, "err", err)
	}
}

var allowanceWindowNames = map[entitlement.PeriodKind]string{
	entitlement.PeriodKindSixHour: "six-hour",
	entitlement.PeriodKindWeekly:  "weekly",
	entitlement.PeriodKindBilling: "monthly",
}

var allowancePlanNames = map[entitlement.Plan]string{
	entitlement.PlanBoost: "Boost",
	entitlement.PlanMax:   "Max",
}

func includedAllowanceSpentMessage(e *IncludedAllowanceSpentError) string {
	message := fmt.Sprintf("Your Weave router %s plan's %s allowance is used up and your subscription can't serve this turn (rate-limited, or the requested model isn't subscription-covered).",
		allowancePlanNames[e.Window.Plan], allowanceWindowNames[e.Window.Kind])
	if !e.Window.ResetsAt.IsZero() {
		message += " Included usage resumes at " + e.Window.ResetsAt.UTC().Format("15:04 MST on Jan 2") + "."
	}
	if errors.Is(e.orgGate, ErrSpendCapSubscriptionUnavailable) {
		return message + " A Weave router spend cap blocks paid routing until then."
	}
	if errors.Is(e.orgGate, ErrSubscriptionUnavailableForTurn) {
		return message
	}
	return message + " To keep routing until then, add credits: " + topUpURL
}

func noEligibleProviderClass() DispatchErrorClass {
	return DispatchErrorClass{
		Kind:       DispatchErrorNoEligibleProvider,
		Status:     http.StatusBadRequest,
		Message:    "No enabled provider can serve any deployed model: add a provider key (BYOK or a provider Authorization header), or re-enable an excluded provider.",
		LogLevel:   "warn",
		LogMessage: "No eligible provider for request",
	}
}
