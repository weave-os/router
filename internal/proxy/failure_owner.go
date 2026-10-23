package proxy

import (
	"context"
	"errors"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

// FailureOwner names who is at fault for a failed attempt or turn, for the
// telemetry failure_owner column. The empty value is a normal completion.
type FailureOwner string

// FailureOwner values. FailureOwnerUnknown marks an error no rule covers yet.
const (
	FailureOwnerProvider FailureOwner = "provider"
	FailureOwnerRouter   FailureOwner = "router"
	FailureOwnerClient   FailureOwner = "client"
	FailureOwnerUnknown  FailureOwner = "unknown"
)

// failureOwnerInput is what classifyFailureOwner needs about one attempt.
type failureOwnerInput struct {
	Err    error
	Status int
	// BodyTranslated is true when the router rewrote the request body it sent
	// this attempt, by format translation or a same-format rewrite.
	BodyTranslated bool
	// CustomerCredential is true when the attempt ran on a credential the
	// customer supplied (BYOK key or their own subscription OAuth).
	CustomerCredential bool
}

// classifyFailureOwner assigns the fault of a failed attempt. Status is the
// upstream HTTP status when one was read; zero falls back to the status the
// error chain carries.
func classifyFailureOwner(in failureOwnerInput) FailureOwner {
	err := in.Err
	if err == nil {
		return ""
	}
	if isUpstreamWatchdogError(err) || isStreamTerminalMissing(err) || providers.IsUpstreamCyberPolicyRefusal(err) {
		return FailureOwnerProvider
	}
	// A committed stream the client abandoned is rendered as a synthetic 502;
	// the cancellation, not the status, says who ended it.
	if errors.Is(err, context.Canceled) {
		return FailureOwnerClient
	}
	if errors.Is(err, translate.ErrCustomToolArguments) {
		return FailureOwnerRouter
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

// upstreamStatusOwner assigns the fault of an upstream HTTP error status.
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

// streamFailureOwner assigns the fault of a transport failure. A watchdog
// abort is the provider's: the stream stopped making progress.
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

// dispatchErrorKindOwner assigns the fault of a classified dispatch error.
// DispatchErrorUpstreamStatus depends on the status and is resolved by
// upstreamStatusOwner instead.
func dispatchErrorKindOwner(kind DispatchErrorKind) FailureOwner {
	switch kind {
	case DispatchErrorSubscriptionPoolExhausted, DispatchErrorNotImplemented, DispatchErrorProviderNotConfigured, DispatchErrorRLPolicyUnavailable,
		DispatchErrorBanditUnavailable, DispatchErrorHMMUnavailable, DispatchErrorPolicyUnavailable,
		DispatchErrorClusterUnavailable, DispatchErrorDecisionModelUnavailable, DispatchErrorClassifierUnavailable,
		DispatchErrorPolicyPinUnavailable, DispatchErrorPlanTargetMismatch, DispatchErrorPlanUnresolvable,
		DispatchErrorSpendLimitUnavailable, DispatchErrorSubscriptionPoolUnavailable,
		DispatchErrorTranslationIntrinsicallyIncompatible, DispatchErrorTranslationProviderUnavailable,
		DispatchErrorRoutedModelIncompatible:
		return FailureOwnerRouter
	case DispatchErrorRequestNotJSONObject, DispatchErrorResponsesChatCompletionsBody, DispatchErrorContextWindowExceeded,
		DispatchErrorInvalidRoutingKnobs, DispatchErrorAnthropicCacheControlInvalid, DispatchErrorForcedModelExcluded,
		DispatchErrorForcedModelUnknown, DispatchErrorPassthroughModelUnknown, DispatchErrorForcedClusterUnsupportedStrategy,
		DispatchErrorForcedClusterUnservable, DispatchErrorPlanOverrideRejected, DispatchErrorHandoff,
		DispatchErrorClassifierHistory, DispatchErrorClassifierInputTooLong, DispatchErrorCreditsExhausted,
		DispatchErrorUserSpendLimitReached, DispatchErrorSpendCapReached, DispatchErrorNoEligibleProvider,
		DispatchErrorAllowlistEmptiesPool, DispatchErrorGatewayServesNoModel, DispatchErrorNoRoutableModels,
		DispatchErrorProductIneligible, DispatchErrorSubscriptionClientAppRefused:
		return FailureOwnerClient
	default:
		return FailureOwnerUnknown
	}
}

// turnFailureOwner assigns the fault of a turn that completed with unusable
// output, from its error class. A refusal is the model's choice; hitting the
// output cap is a served completion, not a failure.
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
