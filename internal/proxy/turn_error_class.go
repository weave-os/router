package proxy

import (
	"errors"
	"net/http"

	"weave-os/router/internal/ingress"
)

type TurnErrorClass string

const (
	TurnErrorRateLimited     TurnErrorClass = "rate_limited"
	TurnErrorInvalidRequest  TurnErrorClass = "invalid_request"
	TurnErrorAuthRejected    TurnErrorClass = "auth_rejected"
	TurnErrorModelNotFound   TurnErrorClass = "model_not_found"
	TurnErrorUpstream4xx     TurnErrorClass = "upstream_4xx"
	TurnErrorUpstream5xx     TurnErrorClass = "upstream_5xx"
	TurnErrorStreamCut       TurnErrorClass = "stream_cut"
	TurnErrorStreamStalled   TurnErrorClass = "stream_stalled"
	TurnErrorTimeout         TurnErrorClass = "timeout"
	TurnErrorClientCanceled  TurnErrorClass = "client_canceled"
	TurnErrorRoutingRefused  TurnErrorClass = "routing_refused"
	TurnErrorFundingRefused  TurnErrorClass = "funding_refused"
	TurnErrorOther           TurnErrorClass = "other"
	TurnErrorRefusal         TurnErrorClass = "refusal"
	TurnErrorMaxTokens       TurnErrorClass = "max_tokens"
	TurnErrorInvalidToolArgs TurnErrorClass = "invalid_tool_args"
)

func classifyTurnError(err error, stopReason string, invalidToolArgsBlocks int) TurnErrorClass {
	if err != nil {
		if isUpstreamWatchdogError(err) {
			return TurnErrorStreamStalled
		}
		if isStreamTerminalMissing(err) {
			return TurnErrorStreamCut
		}
		if status := upstreamStatus(err); status != 0 {
			return classifyUpstreamStatus(status)
		}
		if class, ok := fundingRefusalTurnClass(err); ok {
			return class
		}
		switch classifyStreamFailure(err, "") {
		case streamFailureIdleWatchdog, streamFailureOutputStallWatchdog, streamFailureSlowThroughputWatchdog:
			return TurnErrorStreamStalled
		case streamFailureClientCanceled:
			return TurnErrorClientCanceled
		case streamFailureDeadline, streamFailureUpstreamTimeout:
			return TurnErrorTimeout
		case streamFailureUpstreamEOF, streamFailureUpstreamReset, streamFailureUpstreamErrorFrame:
			return TurnErrorStreamCut
		default:
			return TurnErrorOther
		}
	}
	switch stopReason {
	case "refusal", "content_filter":
		return TurnErrorRefusal
	case "max_tokens", "length":
		return TurnErrorMaxTokens
	}
	if invalidToolArgsBlocks > 0 {
		return TurnErrorInvalidToolArgs
	}
	return ""
}

func classifyUpstreamStatus(status int) TurnErrorClass {
	switch {
	case status == http.StatusTooManyRequests:
		return TurnErrorRateLimited
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return TurnErrorAuthRejected
	case status == http.StatusNotFound:
		return TurnErrorModelNotFound
	case status == http.StatusBadRequest, status == http.StatusRequestEntityTooLarge, status == http.StatusUnprocessableEntity:
		return TurnErrorInvalidRequest
	case status >= 500:
		return TurnErrorUpstream5xx
	case status >= 400:
		return TurnErrorUpstream4xx
	default:
		return TurnErrorOther
	}
}

func fundingRefusalTurnClass(err error) (TurnErrorClass, bool) {
	cls, ok := ClassifyDispatchError(err)
	if !ok {
		return "", false
	}
	switch cls.Kind {
	case ingress.DispatchErrorSubscriptionPoolThrottled:
		return TurnErrorRateLimited, true
	case ingress.DispatchErrorSubscriptionPoolExhausted:
		if errors.Is(err, errLinkedSeatsThrottled) {
			return TurnErrorRateLimited, true
		}
		return TurnErrorFundingRefused, true
	case ingress.DispatchErrorCreditsExhausted, ingress.DispatchErrorSpendCapReached, ingress.DispatchErrorUserSpendLimitReached,
		ingress.DispatchErrorWeaveCreditsDisabled, ingress.DispatchErrorSubscriptionUnavailableForTurn, ingress.DispatchErrorClaudeEnterpriseOnly:
		return TurnErrorFundingRefused, true
	default:
		return "", false
	}
}
