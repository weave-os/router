package proxy

import (
	"net/http"
	"weave-os/router/internal/telemetry/turnrecord"
)

func classifyTurnError(err error, stopReason string, invalidToolArgsBlocks int) turnrecord.TurnErrorClass {
	if err != nil {
		if isUpstreamWatchdogError(err) {
			return turnrecord.TurnErrorStreamStalled
		}
		if isStreamTerminalMissing(err) {
			return turnrecord.TurnErrorStreamCut
		}
		if status := upstreamStatus(err); status != 0 {
			return classifyUpstreamStatus(status)
		}
		switch classifyStreamFailure(err, "") {
		case streamFailureIdleWatchdog, streamFailureOutputStallWatchdog, streamFailureSlowThroughputWatchdog:
			return turnrecord.TurnErrorStreamStalled
		case streamFailureClientCanceled:
			return turnrecord.TurnErrorClientCanceled
		case streamFailureDeadline, streamFailureUpstreamTimeout:
			return turnrecord.TurnErrorTimeout
		case streamFailureUpstreamEOF, streamFailureUpstreamReset, streamFailureUpstreamErrorFrame:
			return turnrecord.TurnErrorStreamCut
		default:
			return turnrecord.TurnErrorOther
		}
	}
	switch stopReason {
	case "refusal", "content_filter":
		return turnrecord.TurnErrorRefusal
	case "max_tokens", "length":
		return turnrecord.TurnErrorMaxTokens
	}
	if invalidToolArgsBlocks > 0 {
		return turnrecord.TurnErrorInvalidToolArgs
	}
	return ""
}

func classifyUpstreamStatus(status int) turnrecord.TurnErrorClass {
	switch {
	case status == http.StatusTooManyRequests:
		return turnrecord.TurnErrorRateLimited
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return turnrecord.TurnErrorAuthRejected
	case status == http.StatusNotFound:
		return turnrecord.TurnErrorModelNotFound
	case status == http.StatusBadRequest, status == http.StatusRequestEntityTooLarge, status == http.StatusUnprocessableEntity:
		return turnrecord.TurnErrorInvalidRequest
	case status >= 500:
		return turnrecord.TurnErrorUpstream5xx
	case status >= 400:
		return turnrecord.TurnErrorUpstream4xx
	default:
		return turnrecord.TurnErrorOther
	}
}
