// Package escalation defines the optional classifier's session and routing contracts.
package escalation

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// Group is an ordinal complexity class, independent of model catalog tiers.
type Group string

const (
	Low     Group = "low"
	Medium  Group = "medium"
	High    Group = "high"
	Maximum Group = "maximum"
)

// Rank returns -1 for a class outside the supported taxonomy.
func Rank(group Group) int {
	switch group {
	case Low:
		return 0
	case Medium:
		return 1
	case High:
		return 2
	case Maximum:
		return 3
	}
	return -1
}

// Higher preserves the stronger of a base classification and a session floor.
func Higher(base, floor Group) Group {
	if Rank(floor) > Rank(base) {
		return floor
	}
	return base
}

// Next returns exactly the adjacent class, capped at maximum.
func Next(group Group) Group {
	switch group {
	case Low:
		return Medium
	case Medium:
		return High
	case High:
		return Maximum
	default:
		return group
	}
}

// Constraint carries classifier intent without overriding user/provider restrictions.
type Constraint struct {
	Floor    Group
	Escalate bool
}

// Outcome describes why an optional classification intervention did or did not apply.
type Outcome string

const (
	OutcomePromoted       Outcome = "promoted"
	OutcomeFloor          Outcome = "floor_applied"
	OutcomeBelowThreshold Outcome = "below_threshold"
	OutcomeMaximum        Outcome = "at_maximum"
	OutcomeNoTarget       Outcome = "no_eligible_target"
	OutcomeUnavailable    Outcome = "unavailable"
	OutcomeObserved       Outcome = "observed"
	OutcomeShadow         Outcome = "shadow"
)

// Decision preserves baseline classification separately from the effective class.
type Decision struct {
	Baseline    Group   `json:"baseline"`
	Effective   Group   `json:"effective"`
	Outcome     Outcome `json:"outcome"`
	Constrained bool    `json:"constrained"`
}

// Prediction is the immutable package's raw-score operating decision.
type Prediction struct {
	Score     float64 `json:"score"`
	Threshold float64 `json:"threshold"`
	Escalate  bool    `json:"escalate"`
}

// Validate rejects corrupt scores or a disagreement with the versioned threshold.
func (p Prediction) Validate() error {
	if math.IsNaN(p.Score) || math.IsInf(p.Score, 0) || p.Score < 0 || p.Score > 1 || math.IsNaN(p.Threshold) || math.IsInf(p.Threshold, 0) || p.Threshold < 0 || p.Threshold > 1 || p.Escalate != (p.Score >= p.Threshold) {
		return errors.New("invalid escalation prediction")
	}
	return nil
}

// PreviousOutcome is evidence available only after the preceding dispatch completes.
type PreviousOutcome struct {
	IsError    bool            `json:"is_error"`
	StatusCode int             `json:"status_code"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
}

// ObserveRequest updates the persisted feature state and optionally scores a checkpoint.
type ObserveRequest struct {
	Observation     json.RawMessage  `json:"observation"`
	State           json.RawMessage  `json:"state"`
	PreviousOutcome *PreviousOutcome `json:"previous_outcome"`
	PredictDue      bool             `json:"predict_due"`
}

type SchemaVersion string

const SchemaVersionV1 SchemaVersion = "escalation_runtime_v1"

// ObserveResponse identifies the package that produced the state and score.
type ObserveResponse struct {
	PredictionUnavailable bool            `json:"prediction_unavailable"`
	SchemaVersion         SchemaVersion   `json:"schema_version"`
	State                 json.RawMessage `json:"state"`
	Prediction            *Prediction     `json:"prediction"`
	ModelID               string          `json:"model_id"`
	PackageSHA256         string          `json:"package_sha256"`
}

// Validate parses the optional service boundary before any state is persisted.
func (r ObserveResponse) Validate(due bool) error {
	if r.SchemaVersion != SchemaVersionV1 || r.ModelID == "" || len(r.PackageSHA256) != 64 || len(r.State) == 0 || !json.Valid(r.State) || string(r.State) == "null" {
		return errors.New("invalid escalation response contract")
	}
	if _, err := hex.DecodeString(r.PackageSHA256); err != nil {
		return errors.New("invalid escalation package digest")
	}
	if r.PredictionUnavailable && (!due || r.Prediction != nil) {
		return errors.New("invalid unavailable prediction")
	}
	if !r.PredictionUnavailable && due != (r.Prediction != nil) {
		return fmt.Errorf("escalation prediction cadence mismatch: due=%t", due)
	}
	if r.Prediction != nil {
		return r.Prediction.Validate()
	}
	return nil
}

// Observer is implemented by the authenticated policy HTTP adapter.
type Observer interface {
	ObserveEscalation(context.Context, ObserveRequest) (ObserveResponse, error)
}

// Session is the opaque feature reducer and durable routing floor for one activation.
type Session struct {
	FeatureTurns    int64            `json:"feature_turns"`
	Ordinal         int64            `json:"ordinal"`
	FeatureState    json.RawMessage  `json:"feature_state"`
	Floor           Group            `json:"floor"`
	ModelID         string           `json:"model_id"`
	PackageSHA256   string           `json:"package_sha256"`
	PreviousOutcome *PreviousOutcome `json:"previous_outcome"`
}

// Checkpoint records a logical boundary independently of mutable artifact pins.
type Checkpoint struct {
	Ordinal    int64       `json:"ordinal"`
	Prediction *Prediction `json:"prediction,omitempty"`
	Decision   *Decision   `json:"decision,omitempty"`
}

// ErrLeaseLost prevents a late request from overwriting another observation.
var ErrLeaseLost = errors.New("escalation observation lease lost")

// Store persists short claims; no database lock is held while inference runs.
type Store interface {
	Claim(ctx context.Context, scope [32]byte, installationID, token string, boundary [32]byte) (Session, bool, error)
	Checkpoint(ctx context.Context, scope, boundary [32]byte) (Checkpoint, bool, error)
	Commit(ctx context.Context, scope, boundary [32]byte, token string, session Session, checkpoint Checkpoint) error
	Release(ctx context.Context, scope [32]byte, token string) error
	Invalidate(ctx context.Context, scope, boundary [32]byte, failedToken string) error
	SaveOutcome(ctx context.Context, scope [32]byte, ordinal int64, outcome PreviousOutcome) error
	SaveContinuation(ctx context.Context, activation [32]byte, responseID string, scope [32]byte, ordinal int64, history json.RawMessage) error
	Continuation(ctx context.Context, activation [32]byte, responseID string) (scope [32]byte, history json.RawMessage, found bool, err error)
	SweepExpired(context.Context) error
}
