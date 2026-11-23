package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/ingress"
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
	if errors.Is(err, credentials.ErrCallerPlanThrottled) {
		return turnrecord.FailureOwnerClient
	}
	if cls, ok := ClassifyDispatchError(err); ok && cls.Kind != ingress.DispatchErrorUpstreamStatus {
		switch {
		case cls.Kind == ingress.DispatchErrorHandoff && errors.Is(err, conversation.ErrHandoffUnavailable):
			return turnrecord.FailureOwnerRouter
		case cls.Kind == ingress.DispatchErrorSubscriptionPoolExhausted && (upstreamStatus(err) == http.StatusTooManyRequests || errors.Is(err, credentials.ErrLinkedSeatsThrottled)):
			return turnrecord.FailureOwnerProvider
		case cls.Kind == ingress.DispatchErrorSubscriptionPoolExhausted && errors.Is(err, credentials.ErrLinkedSeatsUnusable):
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

func dispatchErrorKindOwner(kind ingress.DispatchErrorKind) turnrecord.FailureOwner {
	switch kind {
	case ingress.DispatchErrorSubscriptionPoolExhausted, ingress.DispatchErrorNotImplemented, ingress.DispatchErrorProviderNotConfigured, ingress.DispatchErrorAnthropicFederationUnavailable,
		ingress.DispatchErrorHMMUnavailable, ingress.DispatchErrorPolicyUnavailable,
		ingress.DispatchErrorClusterUnavailable, ingress.DispatchErrorDecisionModelUnavailable, ingress.DispatchErrorClassifierUnavailable,
		ingress.DispatchErrorPolicyPinUnavailable, ingress.DispatchErrorPlanTargetMismatch, ingress.DispatchErrorPlanUnresolvable,
		ingress.DispatchErrorSpendLimitUnavailable, ingress.DispatchErrorSubscriptionPoolUnavailable,
		ingress.DispatchErrorTranslationIntrinsicallyIncompatible, ingress.DispatchErrorTranslationProviderUnavailable,
		ingress.DispatchErrorRoutedModelIncompatible, ingress.DispatchErrorNoEligibleArm:
		return turnrecord.FailureOwnerRouter
	case ingress.DispatchErrorCompactionUnservable, ingress.DispatchErrorSubscriptionPoolThrottled:
		return turnrecord.FailureOwnerProvider
	case ingress.DispatchErrorRequestNotJSONObject, ingress.DispatchErrorResponsesChatCompletionsBody, ingress.DispatchErrorContextWindowExceeded, ingress.DispatchErrorImageLimitExceeded,
		ingress.DispatchErrorInvalidRoutingKnobs, ingress.DispatchErrorAnthropicCacheControlInvalid, ingress.DispatchErrorForcedModelExcluded,
		ingress.DispatchErrorForcedModelUnknown, ingress.DispatchErrorPassthroughModelUnknown, ingress.DispatchErrorForcedClusterUnsupportedStrategy,
		ingress.DispatchErrorForcedClusterUnservable, ingress.DispatchErrorPlanOverrideRejected, ingress.DispatchErrorHandoff,
		ingress.DispatchErrorClassifierHistory, ingress.DispatchErrorClassifierInputTooLong, ingress.DispatchErrorCreditsExhausted,
		ingress.DispatchErrorUserSpendLimitReached, ingress.DispatchErrorSpendCapReached, ingress.DispatchErrorNoEligibleProvider,
		ingress.DispatchErrorAllowlistEmptiesPool, ingress.DispatchErrorGatewayServesNoModel, ingress.DispatchErrorNoRoutableModels,
		ingress.DispatchErrorProductIneligible, ingress.DispatchErrorSubscriptionClientAppRefused, ingress.DispatchErrorClaudeEnterpriseOnly,
		ingress.DispatchErrorWeaveCreditsDisabled, ingress.DispatchErrorSubscriptionUnavailableForTurn:
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
