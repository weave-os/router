package ingress

import (
	"log/slog"
	"weave-os/router/internal/refusal"
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
