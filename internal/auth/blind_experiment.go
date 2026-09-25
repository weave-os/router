package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"
)

const blindExperimentBucketCount = 10_000

// BlindExperimentArm is the effective routing behavior assigned to a subject.
type BlindExperimentArm string

const (
	// BlindExperimentArmRouterOn keeps the router's normal automatic selection.
	BlindExperimentArmRouterOn BlindExperimentArm = "router_on"
	// BlindExperimentArmPassthrough dispatches the caller's requested model directly.
	BlindExperimentArmPassthrough BlindExperimentArm = "passthrough"
)

// Valid reports whether arm is a supported effective experiment arm.
func (arm BlindExperimentArm) Valid() bool {
	return arm == BlindExperimentArmRouterOn || arm == BlindExperimentArmPassthrough
}

// BlindExperimentAssignmentSource records whether an effective arm came from
// deterministic allocation or a manager override.
type BlindExperimentAssignmentSource string

type CohortBypassReason string

const (
	CohortBypassUnassigned    CohortBypassReason = "unassigned"
	CohortBypassDisabled      CohortBypassReason = "disabled"
	CohortBypassOutsideWindow CohortBypassReason = "outside_window"
	CohortBypassHardPin       CohortBypassReason = "hard_pin"
	CohortBypassForceModel    CohortBypassReason = "force_model"
	CohortBypassUsageBypass   CohortBypassReason = "usage_bypass"
	CohortBypassNotDispatched CohortBypassReason = "not_dispatched"
)

const (
	// BlindExperimentAssignmentAutomatic is a deterministic seed/subject assignment.
	BlindExperimentAssignmentAutomatic BlindExperimentAssignmentSource = "automatic"
	// BlindExperimentAssignmentManual is a manager-selected override.
	BlindExperimentAssignmentManual BlindExperimentAssignmentSource = "manual"
)

// BlindExperimentState is the active request-scoped experiment decision.
// Inactive states are cached but never stashed on a request context.
type BlindExperimentState struct {
	Active              bool
	Enabled             bool
	Arm                 BlindExperimentArm
	AssignmentSource    BlindExperimentAssignmentSource
	CanonicalSubjectKey string
	CohortExperimentID  string
	CohortGroupID       int
	CohortRevision      int
	CohortPhaseIndex    int
	ScheduledArm        BlindExperimentArm
	CohortStartsAt      time.Time
	CohortEndsAt        time.Time
	CohortSchedule      []BlindExperimentPhase
	CohortOverrides     []BlindExperimentEmergencyOverride
	ManualOverride      BlindExperimentArm
}

// BlindExperimentEmergencyOverride temporarily replaces the scheduled arm.
// The cohort membership and scheduled treatment never change.
type BlindExperimentEmergencyOverride struct {
	StartsAt time.Time          `json:"starts_at"`
	EndsAt   time.Time          `json:"ends_at"`
	Arm      BlindExperimentArm `json:"arm"`
}

// BlindExperimentPhase is one immutable group treatment interval.
type BlindExperimentPhase struct {
	Index    int                `json:"phase_index"`
	StartsAt time.Time          `json:"starts_at"`
	EndsAt   time.Time          `json:"ends_at"`
	Arm      BlindExperimentArm `json:"arm"`
}

// BlindExperimentRecord is the repository projection needed to resolve one user.
type BlindExperimentRecord struct {
	Configured          bool
	Enabled             bool
	RouterOnPercentage  int
	Seed                string
	CanonicalSubjectKey string
	AutomaticArm        BlindExperimentArm
	ManualOverride      BlindExperimentArm
	CohortExperimentID  string
	CohortStartsAt      time.Time
	CohortEndsAt        time.Time
	CohortRevision      int
	CohortGroupID       int
	CohortSchedule      []BlindExperimentPhase
	CohortOverrides     []BlindExperimentEmergencyOverride
}

// BlindExperimentRepository reads experiment state owned by the control plane.
type BlindExperimentRepository interface {
	GetForUser(ctx context.Context, installationID, routerUserID string) (BlindExperimentRecord, error)
}

// BlindExperimentContextKey is the request-context key for an active assignment.
type BlindExperimentContextKey struct{}

// BlindExperimentFrom returns the active experiment assignment on ctx.
func BlindExperimentFrom(ctx context.Context) (BlindExperimentState, bool) {
	state, ok := ctx.Value(BlindExperimentContextKey{}).(BlindExperimentState)
	return state, ok && state.Active
}

// BlindExperimentStatusFrom includes inactive cohort states for the private
// analytics export; routing only consults BlindExperimentFrom.
func BlindExperimentStatusFrom(ctx context.Context) (BlindExperimentState, bool) {
	state, ok := ctx.Value(BlindExperimentContextKey{}).(BlindExperimentState)
	return state, ok
}

// AssignBlindExperimentArm deterministically assigns a subject to a stable
// 10,000-bucket cohort. Boundary percentages are exact and avoid hash work.
func AssignBlindExperimentArm(seed, canonicalSubjectKey string, routerOnPercentage int) BlindExperimentArm {
	if routerOnPercentage <= 0 {
		return BlindExperimentArmPassthrough
	}
	if routerOnPercentage >= 100 {
		return BlindExperimentArmRouterOn
	}
	digest := sha256.Sum256([]byte(seed + "\x00" + canonicalSubjectKey))
	bucket := binary.BigEndian.Uint64(digest[:8]) % blindExperimentBucketCount
	if bucket < uint64(routerOnPercentage*blindExperimentBucketCount/100) {
		return BlindExperimentArmRouterOn
	}
	return BlindExperimentArmPassthrough
}

func resolveBlindExperiment(record BlindExperimentRecord, routerUserID string) BlindExperimentState {
	if !record.Configured {
		return BlindExperimentState{}
	}
	subjectKey := record.CanonicalSubjectKey
	if subjectKey == "" {
		subjectKey = routerUserID
	}
	if record.CohortExperimentID != "" {
		return BlindExperimentState{
			Enabled:             record.Enabled,
			CanonicalSubjectKey: subjectKey,
			CohortExperimentID:  record.CohortExperimentID,
			CohortGroupID:       record.CohortGroupID,
			CohortRevision:      record.CohortRevision,
			CohortStartsAt:      record.CohortStartsAt,
			CohortEndsAt:        record.CohortEndsAt,
			CohortSchedule:      record.CohortSchedule,
			CohortOverrides:     record.CohortOverrides,
			ManualOverride:      record.ManualOverride,
		}
	}
	if !record.Enabled {
		return BlindExperimentState{}
	}
	automaticArm := record.AutomaticArm
	if !automaticArm.Valid() {
		automaticArm = AssignBlindExperimentArm(record.Seed, subjectKey, record.RouterOnPercentage)
	}
	if record.ManualOverride.Valid() {
		return BlindExperimentState{
			Active:              true,
			Arm:                 record.ManualOverride,
			AssignmentSource:    BlindExperimentAssignmentManual,
			CanonicalSubjectKey: subjectKey,
		}
	}
	return BlindExperimentState{
		Active:              true,
		Arm:                 automaticArm,
		AssignmentSource:    BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: subjectKey,
	}
}

// AtTime resolves a cached cohort schedule for this request, including the
// exact boundary where one phase ends and the next begins.
func (state BlindExperimentState) AtTime(now time.Time) BlindExperimentState {
	if state.CohortExperimentID == "" {
		return state
	}
	state.Active = false
	if !state.Enabled || state.CohortGroupID <= 0 || now.Before(state.CohortStartsAt) || !now.Before(state.CohortEndsAt) {
		return state
	}
	for _, phase := range state.CohortSchedule {
		if now.Before(phase.StartsAt) || !now.Before(phase.EndsAt) {
			continue
		}
		state.Active = true
		state.CohortPhaseIndex = phase.Index
		state.ScheduledArm = phase.Arm
		state.Arm = phase.Arm
		state.AssignmentSource = BlindExperimentAssignmentAutomatic
		if state.ManualOverride.Valid() {
			state.Arm = state.ManualOverride
			state.AssignmentSource = BlindExperimentAssignmentManual
		}
		for _, override := range state.CohortOverrides {
			if !now.Before(override.StartsAt) && now.Before(override.EndsAt) && override.Arm.Valid() {
				state.Arm = override.Arm
				state.AssignmentSource = BlindExperimentAssignmentManual
				break
			}
		}
		return state
	}
	return state
}
