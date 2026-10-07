package proxy

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"weave-os/router/internal/router/turntype"
)

type ContextEstimateKind string

const ContextEstimateApproximate ContextEstimateKind = "approximate"
const ContextSnapshotVersion = 1
const ContextSnapshotTTL = 5 * time.Minute
const maxContextSnapshotTokens = 2_147_483_647

// ContextSnapshot describes a single Router request, never client-local usage.
type ContextSnapshot struct {
	Version             int                 `json:"version"`
	EstimateKind        ContextEstimateKind `json:"estimate_kind"`
	EstimateTokens      int                 `json:"estimate_tokens"`
	ContextWindow       int                 `json:"context_window"`
	OutputReserveTokens int                 `json:"output_reserve_tokens"`
	RequestedModel      string              `json:"requested_model,omitempty"`
	ServedModel         string              `json:"served_model"`
	RequestID           string              `json:"request_id"`
	RequestedAt         time.Time           `json:"requested_at"`
	RecordedAt          time.Time           `json:"recorded_at"`
}

func (snapshot ContextSnapshot) Fresh(now time.Time) bool {
	return snapshot.Version == ContextSnapshotVersion && snapshot.EstimateKind == ContextEstimateApproximate &&
		snapshot.EstimateTokens > 0 && snapshot.EstimateTokens <= maxContextSnapshotTokens &&
		snapshot.ContextWindow > 0 && snapshot.ContextWindow <= maxContextSnapshotTokens &&
		snapshot.OutputReserveTokens > 0 && snapshot.OutputReserveTokens <= maxContextSnapshotTokens &&
		snapshot.ServedModel != "" && len(snapshot.ServedModel) <= 128 && len(snapshot.RequestedModel) <= 128 &&
		snapshot.RequestID != "" && len(snapshot.RequestID) <= 128 &&
		!snapshot.RequestedAt.IsZero() && !snapshot.RecordedAt.Before(snapshot.RequestedAt) &&
		!snapshot.RecordedAt.After(now) && now.Sub(snapshot.RecordedAt) <= ContextSnapshotTTL
}

func ParseContextSnapshot(encoded []byte) *ContextSnapshot {
	if len(encoded) == 0 || len(encoded) > 2048 {
		return nil
	}
	var snapshot ContextSnapshot
	if json.Unmarshal(encoded, &snapshot) != nil {
		return nil
	}
	return &snapshot
}

func setContextEstimateHeaders(headers http.Header, estimate, reserve int) {
	for _, name := range []string{HeaderRouterContextEstimate, HeaderRouterContextReserve, HeaderRouterContextEstimateKind, HeaderRouterContextVersion} {
		headers.Del(name)
	}
	if estimate <= 0 {
		return
	}
	headers.Set(HeaderRouterContextEstimate, strconv.Itoa(estimate))
	headers.Set(HeaderRouterContextEstimateKind, string(ContextEstimateApproximate))
	headers.Set(HeaderRouterContextVersion, strconv.Itoa(ContextSnapshotVersion))
	if reserve > 0 {
		headers.Set(HeaderRouterContextReserve, strconv.Itoa(reserve))
	}
}

func contextSnapshotJSON(headers http.Header, requestID, requestedModel string, requestedAt, recordedAt time.Time) []byte {
	estimate, _ := strconv.Atoi(headers.Get(HeaderRouterContextEstimate))
	window, _ := strconv.Atoi(headers.Get(HeaderRouterContextWindow))
	reserve, _ := strconv.Atoi(headers.Get(HeaderRouterContextReserve))
	snapshot := ContextSnapshot{
		Version: ContextSnapshotVersion, EstimateKind: ContextEstimateKind(headers.Get(HeaderRouterContextEstimateKind)),
		EstimateTokens: estimate, ContextWindow: window, OutputReserveTokens: reserve,
		RequestedModel: requestedModel, ServedModel: headers.Get(HeaderRouterModel), RequestID: requestID, RequestedAt: requestedAt.UTC(), RecordedAt: recordedAt.UTC(),
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
