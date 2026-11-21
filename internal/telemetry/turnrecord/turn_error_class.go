package turnrecord

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
	TurnErrorOther           TurnErrorClass = "other"
	TurnErrorRefusal         TurnErrorClass = "refusal"
	TurnErrorMaxTokens       TurnErrorClass = "max_tokens"
	TurnErrorInvalidToolArgs TurnErrorClass = "invalid_tool_args"
)
