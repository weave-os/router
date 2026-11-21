package proxy

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry/turnrecord"
)

func setContextEstimateHeaders(headers http.Header, estimate, reserve int) {
	for _, name := range []string{requestcontext.HeaderRouterContextEstimate, requestcontext.HeaderRouterContextReserve, requestcontext.HeaderRouterContextEstimateKind, requestcontext.HeaderRouterContextVersion} {
		headers.Del(name)
	}
	if estimate <= 0 {
		return
	}
	headers.Set(requestcontext.HeaderRouterContextEstimate, strconv.Itoa(estimate))
	headers.Set(requestcontext.HeaderRouterContextEstimateKind, string(turnrecord.ContextEstimateApproximate))
	headers.Set(requestcontext.HeaderRouterContextVersion, strconv.Itoa(turnrecord.ContextSnapshotVersion))
	if reserve > 0 {
		headers.Set(requestcontext.HeaderRouterContextReserve, strconv.Itoa(reserve))
	}
}

func contextSnapshotJSON(headers http.Header, requestID, requestedModel string, requestedAt, recordedAt time.Time) []byte {
	return contextSnapshotJSONForDecision(headers, requestID, requestedModel, headers.Get(requestcontext.HeaderRouterModel), headers.Get(requestcontext.HeaderRouterProvider), requestedAt, recordedAt)
}

func contextSnapshotJSONForDecision(headers http.Header, requestID, requestedModel, servedModel, servedProvider string, requestedAt, recordedAt time.Time) []byte {
	estimate, _ := strconv.Atoi(headers.Get(requestcontext.HeaderRouterContextEstimate))
	reserve, _ := strconv.Atoi(headers.Get(requestcontext.HeaderRouterContextReserve))
	window := contextWindowForRequest(servedModel, servedProvider)
	snapshot := turnrecord.ContextSnapshot{
		Version: turnrecord.ContextSnapshotVersion, EstimateKind: turnrecord.ContextEstimateKind(headers.Get(requestcontext.HeaderRouterContextEstimateKind)),
		EstimateTokens: estimate, ContextWindow: window, OutputReserveTokens: reserve,
		RequestedModel: requestedModel, ServedModel: servedModel, RequestID: requestID, RequestedAt: requestedAt.UTC(), RecordedAt: recordedAt.UTC(),
	}
	if !snapshot.Fresh(recordedAt) {
		return nil
	}
	encoded, _ := json.Marshal(snapshot)
	return encoded
}

func conversationContextTurn(kind turntype.TurnType) bool {
	switch kind {
	case turntype.MainLoop, turntype.ToolResult, turntype.Compaction:
		return true
	default:
		return false
	}
}
