package proxy

import (
	"net/http"
)

// TurnErrorClass buckets how a served turn failed, for the telemetry
// error_class column. The empty value is a normal completion.
type TurnErrorClass string

// TurnErrorClass values: upstream HTTP status buckets, transport failures by
// owner, a refused routing decision, and completed turns whose output was
// unusable (refusal, output cap, malformed tool calls).
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
	TurnErrorOther           TurnErrorClass = "other"
	TurnErrorRefusal         TurnErrorClass = "refusal"
	TurnErrorMaxTokens       TurnErrorClass = "max_tokens"
	TurnErrorInvalidToolArgs TurnErrorClass = "invalid_tool_args"
)

// classifyTurnError names the failure of a turn from its final dispatch error
// and, when the upstream completed, the stop reason and invalid tool-call
// count it reported. A transport failure is bucketed by its owner through
// classifyStreamFailure, so a watchdog abort is not mistaken for a client
// cancel.
func classifyTurnError(err error, stopReason string, invalidToolArgsBlocks int) TurnErrorClass {
	if err != nil {
		if isUpstreamWatchdogError(err) {
			return TurnErrorStreamStalled
		}
		// Ahead of the status: a post-commit cut is rendered as a synthetic 502
		// frame that wraps this verdict. An empty stream shares stream_cut
		// because consumers mirror this vocabulary as a closed enum; the stream
		// failure class keeps the two apart.
		if isStreamTerminalMissing(err) {
			return TurnErrorStreamCut
		}
		if status := upstreamStatus(err); status != 0 {
			return classifyUpstreamStatus(status)
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
