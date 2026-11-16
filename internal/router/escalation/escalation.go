package escalation

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

type Group string

const (
	Low     Group = "low"
	Medium  Group = "medium"
	High    Group = "high"
	Maximum Group = "maximum"
)

const TargetClusterLabel = "escalation"

type Mode string

const (
	ModeActive Mode = "active"
	ModeShadow Mode = "shadow"
)

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

func Higher(base, floor Group) Group {
	if Rank(floor) > Rank(base) {
		return floor
	}
	return base
}

const (
	slidingStepProbability         = 0.30
	slidingFullProbability         = 0.50
	slidingMediumToHighProbability = 0.40
)

func SlidingThresholdTarget(pinned, classified Group, probability float64) (target Group, covered bool) {
	thresholds, covered := SlidingThresholdsFor(pinned, classified)
	if !covered {
		return "", false
	}
	if probability >= thresholds.FullProbability {
		return classified, true
	}
	if probability >= thresholds.StepProbability {
		return thresholds.StepGroup, true
	}
	return "", true
}

// SlidingUpgradeThresholds exposes the same bounds to selection and its explanation.
type SlidingUpgradeThresholds struct {
	StepGroup       Group
	StepProbability float64
	FullProbability float64
}

// SlidingThresholdsFor returns the configured bands without evaluating a vote.
func SlidingThresholdsFor(pinned, classified Group) (SlidingUpgradeThresholds, bool) {
	switch {
	case classified == Maximum && (pinned == Low || pinned == Medium):
		return SlidingUpgradeThresholds{High, slidingStepProbability, slidingFullProbability}, true
	case classified == Maximum && pinned == High:
		return SlidingUpgradeThresholds{"", slidingFullProbability, slidingFullProbability}, true
	case classified == High && pinned == Low:
		return SlidingUpgradeThresholds{Medium, slidingStepProbability, slidingFullProbability}, true
	case classified == High && pinned == Medium:
		return SlidingUpgradeThresholds{High, slidingMediumToHighProbability, slidingMediumToHighProbability}, true
	case classified == Medium && pinned == Low:
		return SlidingUpgradeThresholds{Medium, slidingStepProbability, slidingStepProbability}, true
	}
	return SlidingUpgradeThresholds{}, false
}

type Constraint struct {
	Floor    Group
	Escalate bool
}

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

type Decision struct {
	Baseline    Group   `json:"baseline"`
	Effective   Group   `json:"effective"`
	Outcome     Outcome `json:"outcome"`
	Constrained bool    `json:"constrained"`
}

type Prediction struct {
	Score     float64 `json:"score"`
	Threshold float64 `json:"threshold"`
	Escalate  bool    `json:"escalate"`
}

func (p Prediction) Validate() error {
	if math.IsNaN(p.Score) || math.IsInf(p.Score, 0) || p.Score < 0 || p.Score > 1 || math.IsNaN(p.Threshold) || math.IsInf(p.Threshold, 0) || p.Threshold < 0 || p.Threshold > 1 || p.Escalate != (p.Score >= p.Threshold) {
		return errors.New("invalid escalation prediction")
	}
	return nil
}

type PreviousOutcome struct {
	IsError    bool            `json:"is_error"`
	StatusCode int             `json:"status_code"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
}

type ObserveRequest struct {
	Observation     json.RawMessage  `json:"observation"`
	State           json.RawMessage  `json:"state"`
	PreviousOutcome *PreviousOutcome `json:"previous_outcome"`
	PredictDue      bool             `json:"predict_due"`
}

type SchemaVersion string

const SchemaVersionV1 SchemaVersion = "escalation_runtime_v1"

type ObserveResponse struct {
	PredictionUnavailable bool            `json:"prediction_unavailable"`
	SchemaVersion         SchemaVersion   `json:"schema_version"`
	State                 json.RawMessage `json:"state"`
	Prediction            *Prediction     `json:"prediction"`
	ModelID               string          `json:"model_id"`
	PackageSHA256         string          `json:"package_sha256"`
}

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

type Observer interface {
	ObserveEscalation(context.Context, ObserveRequest) (ObserveResponse, error)
}

type Session struct {
	FeatureTurns    int64            `json:"feature_turns"`
	Ordinal         int64            `json:"ordinal"`
	FeatureState    json.RawMessage  `json:"feature_state"`
	Floor           Group            `json:"floor"`
	ModelID         string           `json:"model_id"`
	PackageSHA256   string           `json:"package_sha256"`
	Mode            Mode             `json:"mode,omitempty"`
	Epoch           *int             `json:"epoch,omitempty"`
	PreviousOutcome *PreviousOutcome `json:"previous_outcome"`
}

type Checkpoint struct {
	Ordinal    int64       `json:"ordinal"`
	Prediction *Prediction `json:"prediction,omitempty"`
	Decision   *Decision   `json:"decision,omitempty"`
}

var ErrLeaseLost = errors.New("escalation observation lease lost")

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
