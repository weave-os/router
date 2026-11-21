package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/ingress"
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
	Err                    error
	Status                 int
	BodyTranslated         bool
	CustomerCredential     bool
	RejectedFieldUnchanged bool
}

func classifyFailureOwner(in failureOwnerInput) FailureOwner {
	err := in.Err
	if err == nil {
		return ""
	}
	if isUpstreamWatchdogError(err) || isStreamTerminalMissing(err) || providers.IsUpstreamCyberPolicyRefusal(err) {
		return FailureOwnerProvider
	}
	if errors.Is(err, context.Canceled) || isOwnLoginRejection(err) {
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
	if cls, ok := ClassifyDispatchError(err); ok && cls.Kind != ingress.DispatchErrorUpstreamStatus {
		switch {
		case cls.Kind == ingress.DispatchErrorHandoff && errors.Is(err, ErrHandoffUnavailable):
			return FailureOwnerRouter
		case cls.Kind == ingress.DispatchErrorSubscriptionPoolExhausted && (upstreamStatus(err) == http.StatusTooManyRequests || errors.Is(err, errLinkedSeatsThrottled)):
			return FailureOwnerProvider
		case cls.Kind == ingress.DispatchErrorSubscriptionPoolExhausted && errors.Is(err, errLinkedSeatsUnusable):
			return FailureOwnerClient
		}
		return dispatchErrorKindOwner(cls.Kind)
	}
	status := in.Status
	if status == 0 {
		status = upstreamStatus(err)
	}
	if status == http.StatusBadRequest && in.RejectedFieldUnchanged && !providers.IsUpstreamCredentialRefusal(err) {
		return FailureOwnerClient
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
	case providers.IsUpstreamProviderBillingBlocked(err), providers.IsUpstreamCredentialRefusal(err):
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

func dispatchErrorKindOwner(kind ingress.DispatchErrorKind) FailureOwner {
	switch kind {
	case ingress.DispatchErrorSubscriptionPoolExhausted, ingress.DispatchErrorNotImplemented, ingress.DispatchErrorProviderNotConfigured, ingress.DispatchErrorAnthropicFederationUnavailable,
		ingress.DispatchErrorHMMUnavailable, ingress.DispatchErrorPolicyUnavailable,
		ingress.DispatchErrorClusterUnavailable, ingress.DispatchErrorDecisionModelUnavailable, ingress.DispatchErrorClassifierUnavailable,
		ingress.DispatchErrorPolicyPinUnavailable, ingress.DispatchErrorPlanTargetMismatch, ingress.DispatchErrorPlanUnresolvable,
		ingress.DispatchErrorSpendLimitUnavailable, ingress.DispatchErrorSubscriptionPoolUnavailable,
		ingress.DispatchErrorTranslationIntrinsicallyIncompatible, ingress.DispatchErrorTranslationProviderUnavailable,
		ingress.DispatchErrorRoutedModelIncompatible, ingress.DispatchErrorNoEligibleArm:
		return FailureOwnerRouter
	case ingress.DispatchErrorCompactionUnservable, ingress.DispatchErrorSubscriptionPoolThrottled:
		return FailureOwnerProvider
	case ingress.DispatchErrorRequestNotJSONObject, ingress.DispatchErrorResponsesChatCompletionsBody, ingress.DispatchErrorContextWindowExceeded, ingress.DispatchErrorImageLimitExceeded,
		ingress.DispatchErrorInvalidRoutingKnobs, ingress.DispatchErrorAnthropicCacheControlInvalid, ingress.DispatchErrorForcedModelExcluded,
		ingress.DispatchErrorForcedModelUnknown, ingress.DispatchErrorPassthroughModelUnknown, ingress.DispatchErrorForcedClusterUnsupportedStrategy,
		ingress.DispatchErrorForcedClusterUnservable, ingress.DispatchErrorPlanOverrideRejected, ingress.DispatchErrorHandoff,
		ingress.DispatchErrorClassifierHistory, ingress.DispatchErrorClassifierInputTooLong, ingress.DispatchErrorCreditsExhausted,
		ingress.DispatchErrorUserSpendLimitReached, ingress.DispatchErrorSpendCapReached, ingress.DispatchErrorNoEligibleProvider,
		ingress.DispatchErrorAllowlistEmptiesPool, ingress.DispatchErrorGatewayServesNoModel, ingress.DispatchErrorNoRoutableModels,
		ingress.DispatchErrorProductIneligible, ingress.DispatchErrorSubscriptionClientAppRefused, ingress.DispatchErrorClaudeEnterpriseOnly,
		ingress.DispatchErrorWeaveCreditsDisabled, ingress.DispatchErrorSubscriptionUnavailableForTurn:
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
