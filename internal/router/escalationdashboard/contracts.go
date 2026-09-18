// Package escalationdashboard defines content-free escalation reporting contracts.
package escalationdashboard

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/router/escalation"
)

const (
	cursorVersion = byte(1)
	cursorBytes   = 21
)

var (
	ErrInvalidCursor = errors.New("invalid escalation dashboard cursor")
	ErrExpiredCursor = errors.New("expired escalation dashboard cursor")
)

// Service identifies the classifier that produced an evaluation.
type Service string

const (
	ServiceXGB        Service = "xgb"
	ServiceSwitchyard Service = "switchyard_llm_v1"
)

// Mode identifies a retained activation's immutable routing behavior.
type Mode string

const (
	ModeActive  Mode = "active"
	ModeShadow  Mode = "shadow"
	ModeUnknown Mode = "unknown"
)

// SessionOutcome limits the session list without changing overview denominators.
type SessionOutcome string

const (
	SessionOutcomeRecommended  SessionOutcome = "recommended"
	SessionOutcomeApplied      SessionOutcome = "applied"
	SessionOutcomeShadow       SessionOutcome = "shadow_recommendation"
	SessionOutcomeNoEvaluation SessionOutcome = "no_evaluation"
)

// Filter selects one retained dashboard cohort and one independently filtered page.
type Filter struct {
	Service        Service
	Mode           Mode
	OrganizationID string
	InstallationID string
	SessionOutcome SessionOutcome
	Limit          int32
	Cursor         string
	CapturedAt     time.Time
	ExpiresAt      time.Time
}

// Summary describes the complete filtered cohort, independent of pagination.
type Summary struct {
	ObservedSessions         int `json:"observed_sessions"`
	EvaluatedSessions        int `json:"evaluated_sessions"`
	RecommendedSessions      int `json:"recommended_sessions"`
	Evaluations              int `json:"evaluations"`
	Recommendations          int `json:"recommendations"`
	EscalationsApplied       int `json:"escalations_applied"`
	ShadowRecommendations    int `json:"shadow_recommendations"`
	FloorConstrainedRequests int `json:"floor_constrained_requests"`
	InvalidEvaluations       int `json:"invalid_evaluations"`
}

// OutcomeBreakdown provides common evaluation counts for a service and mode.
type OutcomeBreakdown struct {
	Service            Service `json:"service"`
	Mode               Mode    `json:"mode"`
	Evaluations        int     `json:"evaluations"`
	Recommendations    int     `json:"recommendations"`
	InvalidEvaluations int     `json:"invalid"`
}

// ProgressBucket groups evaluations by a classifier's native progress unit.
type ProgressBucket struct {
	Service         Service `json:"service"`
	Mode            Mode    `json:"mode"`
	FirstProgress   int     `json:"first_progress"`
	Evaluations     int     `json:"evaluations"`
	Recommendations int     `json:"recommendations"`
}

// FirstRecommendationBucket counts service sessions once at their first recommendation.
type FirstRecommendationBucket struct {
	Service       Service `json:"service"`
	Mode          Mode    `json:"mode"`
	FirstProgress int     `json:"first_progress"`
	Sessions      int     `json:"sessions"`
}

// FloorBucket describes current retained routing floors.
type FloorBucket struct {
	Service  Service           `json:"service"`
	Mode     Mode              `json:"mode"`
	Floor    *escalation.Group `json:"floor"`
	Sessions int               `json:"sessions"`
}

// OrganizationBreakdown provides comparable per-installation aggregates.
type OrganizationBreakdown struct {
	OrganizationID        string  `json:"organization_id"`
	InstallationID        string  `json:"installation_id"`
	Service               Service `json:"service"`
	Mode                  Mode    `json:"mode"`
	ObservedSessions      int     `json:"observed_sessions"`
	EvaluatedSessions     int     `json:"evaluated_sessions"`
	RecommendedSessions   int     `json:"recommended_sessions"`
	Evaluations           int     `json:"evaluations"`
	Recommendations       int     `json:"recommendations"`
	EscalationsApplied    int     `json:"escalations_applied"`
	ShadowRecommendations int     `json:"shadow_recommendations"`
}

// Session is the shared, content-free row shape for both classifiers.
type Session struct {
	ID                       string            `json:"id"`
	Scope                    string            `json:"scope"`
	Lifetime                 string            `json:"lifetime"`
	OrganizationID           string            `json:"organization_id"`
	InstallationID           string            `json:"installation_id"`
	Service                  Service           `json:"service"`
	Mode                     Mode              `json:"mode"`
	Epoch                    *int              `json:"epoch"`
	ObservedProgress         int               `json:"observed_progress"`
	Evaluations              int               `json:"evaluations"`
	Recommendations          int               `json:"recommendations"`
	FirstRecommendation      *int              `json:"first_recommendation"`
	EscalationsApplied       int               `json:"escalations_applied"`
	FloorConstrainedRequests int               `json:"floor_constrained_requests"`
	InvalidEvaluations       int               `json:"invalid_evaluations"`
	Floor                    *escalation.Group `json:"floor"`
	LastActivityAt           time.Time         `json:"last_activity_at"`
	ContinuityBroken         bool              `json:"continuity_broken"`
}

// Snapshot contains one stable retained-state capture.
type Snapshot struct {
	CapturedAt                      time.Time                   `json:"captured_at"`
	Summary                         Summary                     `json:"summary"`
	OutcomeBreakdown                []OutcomeBreakdown          `json:"outcome_breakdown"`
	ProgressDistribution            []ProgressBucket            `json:"progress_distribution"`
	FirstRecommendationDistribution []FirstRecommendationBucket `json:"first_recommendation_distribution"`
	FloorDistribution               []FloorBucket               `json:"floor_distribution"`
	Organizations                   []OrganizationBreakdown     `json:"organizations"`
	Sessions                        []Session                   `json:"sessions"`
	MatchingSessions                int                         `json:"matching_sessions"`
	HasMore                         bool                        `json:"has_more"`
	PageStart                       int                         `json:"page_start"`
	NextCursor                      string                      `json:"next_cursor"`
	PreviousCursor                  string                      `json:"previous_cursor"`
}

// StoredSnapshot carries persistence identity without exposing it on the API.
type StoredSnapshot struct {
	Snapshot
	SnapshotID string `json:"snapshot_id"`
}

// EncodeCursor binds a page position to one immutable snapshot.
func EncodeCursor(snapshotID string, position int32) (string, error) {
	parsedSnapshotID, err := uuid.Parse(snapshotID)
	if err != nil || position < 0 {
		return "", ErrInvalidCursor
	}
	encoded := make([]byte, cursorBytes)
	encoded[0] = cursorVersion
	copy(encoded[1:17], parsedSnapshotID[:])
	binary.BigEndian.PutUint32(encoded[17:], uint32(position))
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// DecodeCursor returns the immutable snapshot and zero-based page position.
func DecodeCursor(cursor string) (string, int32, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) != cursorBytes || decoded[0] != cursorVersion {
		return "", 0, ErrInvalidCursor
	}
	position := binary.BigEndian.Uint32(decoded[17:])
	if position > math.MaxInt32 {
		return "", 0, ErrInvalidCursor
	}
	snapshotID, err := uuid.FromBytes(decoded[1:17])
	if err != nil {
		return "", 0, ErrInvalidCursor
	}
	return snapshotID.String(), int32(position), nil
}

// Store reads normalized operational state without exposing captured content.
type Store interface {
	CreateSnapshot(context.Context, Filter) (StoredSnapshot, error)
	SnapshotPage(context.Context, string, int32, int32, time.Time) (StoredSnapshot, error)
}
