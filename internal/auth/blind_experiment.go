package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

const blindExperimentBucketCount = 10_000

type BlindExperimentArm string

const (
	BlindExperimentArmRouterOn    BlindExperimentArm = "router_on"
	BlindExperimentArmPassthrough BlindExperimentArm = "passthrough"
)

func (arm BlindExperimentArm) Valid() bool {
	return arm == BlindExperimentArmRouterOn || arm == BlindExperimentArmPassthrough
}

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
	BlindExperimentAssignmentAutomatic BlindExperimentAssignmentSource = "automatic"
	BlindExperimentAssignmentManual    BlindExperimentAssignmentSource = "manual"
)

type BlindExperimentMode string

const (
	BlindExperimentModeUser    BlindExperimentMode = "user"
	BlindExperimentModeCohort  BlindExperimentMode = "cohort"
	BlindExperimentModeSession BlindExperimentMode = "session"
)

type BlindExperimentState struct {
	Active                    bool
	Enabled                   bool
	Arm                       BlindExperimentArm
	AssignmentSource          BlindExperimentAssignmentSource
	CanonicalSubjectKey       string
	CohortExperimentID        string
	CohortGroupID             int
	CohortRevision            int
	CohortPhaseIndex          int
	ScheduledArm              BlindExperimentArm
	CohortStartsAt            time.Time
	CohortEndsAt              time.Time
	CohortSchedule            []BlindExperimentPhase
	CohortOverrides           []BlindExperimentEmergencyOverride
	ManualOverride            BlindExperimentArm
	Mode                      BlindExperimentMode
	SessionSubjectKey         string
	sessionSeed               string
	sessionRouterOnPercentage int
}

type BlindExperimentEmergencyOverride struct {
	StartsAt time.Time          `json:"starts_at"`
	EndsAt   time.Time          `json:"ends_at"`
	Arm      BlindExperimentArm `json:"arm"`
}

type BlindExperimentPhase struct {
	Index    int                `json:"phase_index"`
	StartsAt time.Time          `json:"starts_at"`
	EndsAt   time.Time          `json:"ends_at"`
	Arm      BlindExperimentArm `json:"arm"`
}

type BlindExperimentRecord struct {
	Configured          bool
	Enabled             bool
	RouterOnPercentage  int
	Seed                string
	SessionMode         bool
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

type BlindExperimentRepository interface {
	GetForUser(ctx context.Context, installationID, routerUserID string) (BlindExperimentRecord, error)
	InstallationExperimentEnabled(ctx context.Context, installationID string) (bool, error)
}

type BlindExperimentContextKey struct{}

func BlindExperimentFrom(ctx context.Context) (BlindExperimentState, bool) {
	state, ok := ctx.Value(BlindExperimentContextKey{}).(BlindExperimentState)
	return state, ok && state.Active
}

func BlindExperimentStatusFrom(ctx context.Context) (BlindExperimentState, bool) {
	state, ok := ctx.Value(BlindExperimentContextKey{}).(BlindExperimentState)
	return state, ok
}

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
			Mode:                BlindExperimentModeCohort,
		}
	}
	if !record.Enabled {
		return BlindExperimentState{}
	}
	if record.SessionMode {
		return BlindExperimentState{
			Active:                    true,
			Arm:                       AssignBlindExperimentArm(record.Seed, subjectKey, record.RouterOnPercentage),
			AssignmentSource:          BlindExperimentAssignmentAutomatic,
			CanonicalSubjectKey:       subjectKey,
			Mode:                      BlindExperimentModeSession,
			sessionSeed:               record.Seed,
			sessionRouterOnPercentage: record.RouterOnPercentage,
		}
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
			Mode:                BlindExperimentModeUser,
		}
	}
	return BlindExperimentState{
		Active:              true,
		Arm:                 automaticArm,
		AssignmentSource:    BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: subjectKey,
		Mode:                BlindExperimentModeUser,
	}
}

func BlindExperimentSessionSubjectKey(credentialIdentity, clientSessionID string) string {
	if credentialIdentity == "" || clientSessionID == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(credentialIdentity + "\x00" + clientSessionID))
	return "session:" + hex.EncodeToString(digest[:16])
}

func (state BlindExperimentState) ForSession(sessionSubjectKey string) BlindExperimentState {
	if !state.Active || sessionSubjectKey == "" || state.Mode != BlindExperimentModeSession {
		return state
	}
	state.Arm = AssignBlindExperimentArm(state.sessionSeed, sessionSubjectKey, state.sessionRouterOnPercentage)
	state.SessionSubjectKey = sessionSubjectKey
	return state
}

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

type BlindExperimentModelSource string

const (
	BlindExperimentModelSourceRouter                   BlindExperimentModelSource = "router"
	BlindExperimentModelSourceExperimentPassthrough    BlindExperimentModelSource = "experiment_passthrough"
	BlindExperimentModelSourceClientModelPassthrough   BlindExperimentModelSource = "client_model_passthrough"
	BlindExperimentModelSourceRoutingPolicyPassthrough BlindExperimentModelSource = "routing_policy_passthrough"
	BlindExperimentModelSourceHardPin                  BlindExperimentModelSource = "hard_pin"
	BlindExperimentModelSourceForceModel               BlindExperimentModelSource = "force_model"
	BlindExperimentModelSourceUsageBypass              BlindExperimentModelSource = "usage_bypass"
	BlindExperimentModelSourceNotDispatched            BlindExperimentModelSource = "not_dispatched"
)
