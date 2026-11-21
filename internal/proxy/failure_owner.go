package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/translate"
)

type failureOwnerInput struct {
	Err                    error
	Status                 int
	BodyTranslated         bool
	CustomerCredential     bool
	RejectedFieldUnchanged bool
}

func classifyFailureOwner(in failureOwnerInput) turnrecord.FailureOwner {
	err := in.Err
	if err == nil {
		return ""
	}
	if isUpstreamWatchdogError(err) || isStreamTerminalMissing(err) || providers.IsUpstreamCyberPolicyRefusal(err) {
		return turnrecord.FailureOwnerProvider
	}
	if errors.Is(err, context.Canceled) || isOwnLoginRejection(err) {
		return turnrecord.FailureOwnerClient
	}
	if errors.Is(err, translate.ErrCustomToolArguments) {
		return turnrecord.FailureOwnerProvider
	}
	if errors.Is(err, errResponsesPassthroughStuck) {
		return turnrecord.FailureOwnerRouter
	}
	if errors.Is(err, errCallerPlanThrottled) {
		return turnrecord.FailureOwnerClient
	}
	if cls, ok := ClassifyDispatchError(err); ok && cls.Kind != DispatchErrorUpstreamStatus {
		switch {
		case cls.Kind == DispatchErrorHandoff && errors.Is(err, ErrHandoffUnavailable):
			return turnrecord.FailureOwnerRouter
		case cls.Kind == DispatchErrorSubscriptionPoolExhausted && (upstreamStatus(err) == http.StatusTooManyRequests || errors.Is(err, errLinkedSeatsThrottled)):
			return turnrecord.FailureOwnerProvider
		case cls.Kind == DispatchErrorSubscriptionPoolExhausted && errors.Is(err, errLinkedSeatsUnusable):
			return turnrecord.FailureOwnerClient
		}
		return dispatchErrorKindOwner(cls.Kind)
	}
	status := in.Status
	if status == 0 {
		status = upstreamStatus(err)
	}
	if status == http.StatusBadRequest && in.RejectedFieldUnchanged && !providers.IsUpstreamCredentialRefusal(err) {
		return turnrecord.FailureOwnerClient
	}
	if status != 0 {
		return upstreamStatusOwner(status, err, in.BodyTranslated, in.CustomerCredential)
	}
	return streamFailureOwner(classifyStreamFailure(err, ""))
}

func upstreamStatusOwner(status int, err error, bodyTranslated, customerCredential bool) turnrecord.FailureOwner {
	credentialOwner := turnrecord.FailureOwnerRouter
	if customerCredential {
		credentialOwner = turnrecord.FailureOwnerClient
	}
	switch {
	case providers.IsUpstreamProviderBillingBlocked(err), providers.IsUpstreamCredentialRefusal(err):
		return credentialOwner
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusPaymentRequired:
		return credentialOwner
	case status == http.StatusNotFound:
		return turnrecord.FailureOwnerRouter
	case status == http.StatusBadRequest, status == http.StatusRequestEntityTooLarge, status == http.StatusUnprocessableEntity:
		if bodyTranslated {
			return turnrecord.FailureOwnerRouter
		}
		return turnrecord.FailureOwnerProvider
	case status == http.StatusTooManyRequests, status == http.StatusRequestTimeout, status >= 500:
		return turnrecord.FailureOwnerProvider
	default:
		return turnrecord.FailureOwnerUnknown
	}
}

func streamFailureOwner(class streamFailureClass) turnrecord.FailureOwner {
	switch class {
	case streamFailureUpstreamEOF, streamFailureUpstreamReset, streamFailureIdleWatchdog,
		streamFailureOutputStallWatchdog, streamFailureSlowThroughputWatchdog, streamFailureUpstreamErrorFrame,
		streamFailureUpstreamIncomplete, streamFailureUpstreamEmpty, streamFailureUpstreamTimeout, streamFailureDeadline:
		return turnrecord.FailureOwnerProvider
	case streamFailureClientCanceled:
		return turnrecord.FailureOwnerClient
	default:
		return turnrecord.FailureOwnerUnknown
	}
}

func dispatchErrorKindOwner(kind DispatchErrorKind) turnrecord.FailureOwner {
	switch kind {
	case DispatchErrorSubscriptionPoolExhausted, DispatchErrorNotImplemented, DispatchErrorProviderNotConfigured, DispatchErrorAnthropicFederationUnavailable, DispatchErrorRLPolicyUnavailable,
		DispatchErrorHMMUnavailable, DispatchErrorPolicyUnavailable,
		DispatchErrorClusterUnavailable, DispatchErrorDecisionModelUnavailable, DispatchErrorClassifierUnavailable,
		DispatchErrorPolicyPinUnavailable, DispatchErrorPlanTargetMismatch, DispatchErrorPlanUnresolvable,
		DispatchErrorSpendLimitUnavailable, DispatchErrorSubscriptionPoolUnavailable,
		DispatchErrorTranslationIntrinsicallyIncompatible, DispatchErrorTranslationProviderUnavailable,
		DispatchErrorRoutedModelIncompatible, DispatchErrorNoEligibleArm:
		return turnrecord.FailureOwnerRouter
	case DispatchErrorCompactionUnservable, DispatchErrorSubscriptionPoolThrottled:
		return turnrecord.FailureOwnerProvider
	case DispatchErrorRequestNotJSONObject, DispatchErrorResponsesChatCompletionsBody, DispatchErrorContextWindowExceeded, DispatchErrorImageLimitExceeded,
		DispatchErrorInvalidRoutingKnobs, DispatchErrorAnthropicCacheControlInvalid, DispatchErrorForcedModelExcluded,
		DispatchErrorForcedModelUnknown, DispatchErrorPassthroughModelUnknown, DispatchErrorForcedClusterUnsupportedStrategy,
		DispatchErrorForcedClusterUnservable, DispatchErrorPlanOverrideRejected, DispatchErrorHandoff,
		DispatchErrorClassifierHistory, DispatchErrorClassifierInputTooLong, DispatchErrorCreditsExhausted,
		DispatchErrorUserSpendLimitReached, DispatchErrorSpendCapReached, DispatchErrorNoEligibleProvider,
		DispatchErrorAllowlistEmptiesPool, DispatchErrorGatewayServesNoModel, DispatchErrorNoRoutableModels,
		DispatchErrorProductIneligible, DispatchErrorSubscriptionClientAppRefused, DispatchErrorClaudeEnterpriseOnly,
		DispatchErrorWeaveCreditsDisabled, DispatchErrorSubscriptionUnavailableForTurn:
		return turnrecord.FailureOwnerClient
	default:
		return turnrecord.FailureOwnerUnknown
	}
}

func turnFailureOwnerAfterFinish(ctx context.Context, err error, class turnrecord.TurnErrorClass, upstreamFinishReason string) turnrecord.FailureOwner {
	if upstreamFinishReason != "" && isStreamTerminalMissing(err) {
		return turnrecord.FailureOwnerRouter
	}
	return turnFailureOwnerFor(ctx, err, class)
}

func turnFailureOwner(class turnrecord.TurnErrorClass) turnrecord.FailureOwner {
	switch class {
	case "", turnrecord.TurnErrorMaxTokens:
		return ""
	case turnrecord.TurnErrorRefusal, turnrecord.TurnErrorInvalidToolArgs:
		return turnrecord.FailureOwnerProvider
	case turnrecord.TurnErrorRoutingRefused:
		return turnrecord.FailureOwnerRouter
	default:
		return turnrecord.FailureOwnerUnknown
	}
}
