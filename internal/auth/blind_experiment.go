package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	Arm                 BlindExperimentArm
	AssignmentSource    BlindExperimentAssignmentSource
	CanonicalSubjectKey string
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
	if !record.Configured || !record.Enabled {
		return BlindExperimentState{}
	}
	subjectKey := record.CanonicalSubjectKey
	if subjectKey == "" {
		subjectKey = routerUserID
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
