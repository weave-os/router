package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

type FailureOwner string

const (
	FailureOwnerProvider FailureOwner = "provider"
	FailureOwnerRouter   FailureOwner = "router"
	FailureOwnerClient   FailureOwner = "client"
	FailureOwnerUnknown  FailureOwner = "unknown"
)

type failureOwnerInput struct {
	Err                error
	Status             int
	BodyTranslated     bool
	CustomerCredential bool
}

func classifyFailureOwner(in failureOwnerInput) FailureOwner {
	err := in.Err
	if err == nil {
		return ""
	}
	if isUpstreamWatchdogError(err) || isStreamTerminalMissing(err) || providers.IsUpstreamCyberPolicyRefusal(err) {
		return FailureOwnerProvider
	}
	if errors.Is(err, context.Canceled) {
		return FailureOwnerClient
	}
	if errors.Is(err, translate.ErrCustomToolArguments) {
		return FailureOwnerProvider
	}
	if errors.Is(err, errResponsesPassthroughStuck) {
		return FailureOwnerRouter
	}
	if errors.Is(err, errCallerPlanThrottled) {
		return FailureOwnerClient
	}
	if cls, ok := ClassifyDispatchError(err); ok && cls.Kind != DispatchErrorUpstreamStatus {
		if cls.Kind == DispatchErrorHandoff && errors.Is(err, ErrHandoffUnavailable) {
			return FailureOwnerRouter
		}
		return dispatchErrorKindOwner(cls.Kind)
	}
	status := in.Status
	if status == 0 {
		status = upstreamStatus(err)
	}
	if status != 0 {
		return upstreamStatusOwner(status, err, in.BodyTranslated, in.CustomerCredential)
	}
	return streamFailureOwner(classifyStreamFailure(err, ""))
}

func upstreamStatusOwner(status int, err error, bodyTranslated, customerCredential bool) FailureOwner {
	credentialOwner := FailureOwnerRouter
	if customerCredential {
		credentialOwner = FailureOwnerClient
	}
	switch {
	case providers.IsUpstreamProviderBillingBlocked(err):
		return credentialOwner
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusPaymentRequired:
		return credentialOwner
	case status == http.StatusNotFound:
		return FailureOwnerRouter
	case status == http.StatusBadRequest, status == http.StatusRequestEntityTooLarge, status == http.StatusUnprocessableEntity:
		if bodyTranslated {
			return FailureOwnerRouter
		}
		return FailureOwnerProvider
	case status == http.StatusTooManyRequests, status == http.StatusRequestTimeout, status >= 500:
		return FailureOwnerProvider
	default:
		return FailureOwnerUnknown
	}
}

func streamFailureOwner(class streamFailureClass) FailureOwner {
	switch class {
	case streamFailureUpstreamEOF, streamFailureUpstreamReset, streamFailureIdleWatchdog,
		streamFailureOutputStallWatchdog, streamFailureSlowThroughputWatchdog, streamFailureUpstreamErrorFrame,
		streamFailureUpstreamIncomplete, streamFailureUpstreamEmpty, streamFailureUpstreamTimeout, streamFailureDeadline:
		return FailureOwnerProvider
	case streamFailureClientCanceled:
		return FailureOwnerClient
	default:
		return FailureOwnerUnknown
	}
}

func dispatchErrorKindOwner(kind DispatchErrorKind) FailureOwner {
	switch kind {
	case DispatchErrorSubscriptionPoolExhausted, DispatchErrorSubscriptionPoolThrottled, DispatchErrorNotImplemented, DispatchErrorProviderNotConfigured, DispatchErrorAnthropicFederationUnavailable, DispatchErrorRLPolicyUnavailable,
		DispatchErrorHMMUnavailable, DispatchErrorPolicyUnavailable,
		DispatchErrorClusterUnavailable, DispatchErrorDecisionModelUnavailable, DispatchErrorClassifierUnavailable,
		DispatchErrorPolicyPinUnavailable, DispatchErrorPlanTargetMismatch, DispatchErrorPlanUnresolvable,
		DispatchErrorSpendLimitUnavailable, DispatchErrorSubscriptionPoolUnavailable,
		DispatchErrorTranslationIntrinsicallyIncompatible, DispatchErrorTranslationProviderUnavailable,
		DispatchErrorRoutedModelIncompatible, DispatchErrorNoEligibleArm:
		return FailureOwnerRouter
	case DispatchErrorCompactionUnservable:
		return FailureOwnerProvider
	case DispatchErrorRequestNotJSONObject, DispatchErrorResponsesChatCompletionsBody, DispatchErrorContextWindowExceeded, DispatchErrorImageLimitExceeded,
		DispatchErrorInvalidRoutingKnobs, DispatchErrorAnthropicCacheControlInvalid, DispatchErrorForcedModelExcluded,
		DispatchErrorForcedModelUnknown, DispatchErrorPassthroughModelUnknown, DispatchErrorForcedClusterUnsupportedStrategy,
		DispatchErrorForcedClusterUnservable, DispatchErrorPlanOverrideRejected, DispatchErrorHandoff,
		DispatchErrorClassifierHistory, DispatchErrorClassifierInputTooLong, DispatchErrorCreditsExhausted,
		DispatchErrorUserSpendLimitReached, DispatchErrorSpendCapReached, DispatchErrorNoEligibleProvider,
		DispatchErrorAllowlistEmptiesPool, DispatchErrorGatewayServesNoModel, DispatchErrorNoRoutableModels,
		DispatchErrorProductIneligible, DispatchErrorSubscriptionClientAppRefused, DispatchErrorClaudeEnterpriseOnly,
		DispatchErrorWeaveCreditsDisabled, DispatchErrorSubscriptionUnavailableForTurn, DispatchErrorUntranslatableUserContent:
		return FailureOwnerClient
	default:
		return FailureOwnerUnknown
	}
}

func turnFailureOwnerAfterFinish(ctx context.Context, err error, class TurnErrorClass, upstreamFinishReason string) FailureOwner {
	if upstreamFinishReason != "" && isStreamTerminalMissing(err) {
		return FailureOwnerRouter
	}
	return turnFailureOwnerFor(ctx, err, class)
}

func turnFailureOwner(class TurnErrorClass) FailureOwner {
	switch class {
	case "", TurnErrorMaxTokens:
		return ""
	case TurnErrorRefusal, TurnErrorInvalidToolArgs:
		return FailureOwnerProvider
	case TurnErrorRoutingRefused:
		return FailureOwnerRouter
	default:
		return FailureOwnerUnknown
	}
}
