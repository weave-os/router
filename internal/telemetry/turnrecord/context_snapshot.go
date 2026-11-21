package turnrecord

import (
	"encoding/json"
	"time"
)

type ContextEstimateKind string

const ContextEstimateApproximate ContextEstimateKind = "approximate"

const ContextSnapshotVersion = 1

const ContextSnapshotTTL = 5 * time.Minute

const maxContextSnapshotTokens = 2_147_483_647

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
