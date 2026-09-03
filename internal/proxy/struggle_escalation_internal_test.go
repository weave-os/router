package proxy

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/router/policy"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"
)

// fakeRosterSource serves a fixed per-cluster arm roster.
type fakeRosterSource struct {
	clusters map[string][]string
}

func (f fakeRosterSource) ClusterRoster(context.Context) (policy.RosterSnapshot, error) {
	return policy.RosterSnapshot{Clusters: f.clusters}, nil
}

// recordingStruggleStore captures escalation events and serves the budget count.
type recordingStruggleStore struct {
	mu     sync.Mutex
	events []StruggleEscalationEvent
	count  int64
}

func (r *recordingStruggleStore) InsertStruggleEscalationEvent(_ context.Context, p StruggleEscalationEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, p)
	return nil
}

func (r *recordingStruggleStore) CountStruggleEscalationEvents(context.Context, []byte, string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count, nil
}

func struggleTestKey(seed byte) [sessionpin.SessionKeyLen]byte {
	var key [sessionpin.SessionKeyLen]byte
	sum := sha256.Sum256([]byte{seed})
	copy(key[:], sum[:])
	return key
}

// strugglingPin is a pin past the early operating point (turns and wall).
func strugglingPin(model, group string) sessionpin.Pin {
	return sessionpin.Pin{
		Model:         model,
		PolicyGroup:   group,
		Reason:        "hmm_sticky",
		TurnCount:     struggleEarlyTurns,
		FirstPinnedAt: time.Now().Add(-struggleEarlyWall - time.Minute),
	}
}

func newStruggleEscalationSvc(pins *stubPinStore, events *recordingStruggleStore, clusters map[string][]string) *Service {
	return NewService(nil, nil, nil, false, nil, pins, false, "anthropic", "claude-haiku-4-5", nil).
		WithStruggleEscalationConfig(true, 0).
		WithStruggleEscalationStore(events).
		WithStruggleEscalationRoster(NewStruggleRoster(fakeRosterSource{clusters: clusters}))
}

func TestClustersAbove(t *testing.T) {
	assert.Equal(t, []string{"balanced", "medium", "high", "maximum"}, clustersAbove("fast"))
	assert.Equal(t, []string{"balanced", "medium", "high", "maximum"}, clustersAbove("explore"))
	assert.Equal(t, []string{"maximum"}, clustersAbove("high"))
	assert.Nil(t, clustersAbove("maximum"), "the top rung has nothing above it")
	assert.Nil(t, clustersAbove("not-a-cluster"))
}

func TestEscalationTarget_PrefersTheClusterAbove(t *testing.T) {
	roster := NewStruggleRoster(fakeRosterSource{clusters: map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5", "anthropic/claude-sonnet-4-5"},
		"high":     {"anthropic/claude-opus-5"},
	}})

	target, cluster, err := roster.EscalationTarget(
		context.Background(), "balanced", "claude-haiku-4-5", nil, func(string) bool { return true },
	)

	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5", target, "a struggling session must move up, not sideways")
	assert.Equal(t, "high", cluster)
}

func TestEscalationTarget_FallsBackSidewaysWhenNothingAboveIsDispatchable(t *testing.T) {
	roster := NewStruggleRoster(fakeRosterSource{clusters: map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5", "anthropic/claude-sonnet-4-5"},
		"high":     {"anthropic/claude-opus-5"},
	}})

	target, cluster, err := roster.EscalationTarget(
		context.Background(), "balanced", "claude-haiku-4-5", nil,
		func(model string) bool { return model != "claude-opus-5" },
	)

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", target)
	assert.Equal(t, "balanced", cluster)
}

func TestEscalationTarget_TopClusterMovesSideways(t *testing.T) {
	roster := NewStruggleRoster(fakeRosterSource{clusters: map[string][]string{
		"maximum": {"anthropic/claude-opus-5", "anthropic/claude-sonnet-4-5"},
	}})

	target, cluster, err := roster.EscalationTarget(
		context.Background(), "maximum", "claude-opus-5", nil, func(string) bool { return true },
	)

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", target)
	assert.Equal(t, "maximum", cluster)
}

func TestHandleStruggleEscalation_PinsTheClusterAbove(t *testing.T) {
	pins := newStubPinStore()
	pins.getFound = true
	pins.getPin = strugglingPin("claude-haiku-4-5", "balanced")
	events := &recordingStruggleStore{}
	svc := newStruggleEscalationSvc(pins, events, map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5", "anthropic/claude-sonnet-4-5"},
		"high":     {"anthropic/claude-opus-5"},
	})

	svc.handleStruggleEscalation(context.Background(), uuid.New(), struggleTestKey(1), "default", nil)

	require.Len(t, pins.upserts, 1)
	assert.Equal(t, "claude-opus-5", pins.upserts[0].Model)
	assert.Equal(t, "high", pins.upserts[0].PolicyGroup, "the pin must carry the cluster it was escalated into")
	assert.Equal(t, translate.ReasonStruggleEscalation, pins.upserts[0].Reason)

	require.Len(t, events.events, 1)
	assert.Equal(t, struggleActionUpCluster, events.events[0].Action)
	assert.Equal(t, "claude-opus-5", events.events[0].EscalationTarget)
}

func TestHandleStruggleEscalation_SidewaysWhenTheClusterAboveCannotServe(t *testing.T) {
	pins := newStubPinStore()
	pins.getFound = true
	pins.getPin = strugglingPin("claude-haiku-4-5", "balanced")
	events := &recordingStruggleStore{}
	svc := newStruggleEscalationSvc(pins, events, map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5", "anthropic/claude-sonnet-4-5"},
	})

	svc.handleStruggleEscalation(context.Background(), uuid.New(), struggleTestKey(2), "default", nil)

	require.Len(t, pins.upserts, 1)
	assert.Equal(t, "claude-sonnet-4-5", pins.upserts[0].Model)
	assert.Equal(t, "balanced", pins.upserts[0].PolicyGroup)

	require.Len(t, events.events, 1)
	assert.Equal(t, struggleActionSideways, events.events[0].Action)
}

// quietPin sits below the turn/wall thresholds, so only evidence can arm it.
func quietPin(model, group string) sessionpin.Pin {
	return sessionpin.Pin{
		Model:         model,
		PolicyGroup:   group,
		Reason:        "hmm_sticky",
		TurnCount:     struggleEvidenceMinTurns,
		FirstPinnedAt: time.Now().Add(-time.Minute),
	}
}

func TestHandleStruggleEscalation_EvidenceIsInertWhileTheFlagIsOff(t *testing.T) {
	pins := newStubPinStore()
	pins.getFound = true
	pins.getPin = quietPin("claude-haiku-4-5", "balanced")
	events := &recordingStruggleStore{}
	svc := newStruggleEscalationSvc(pins, events, map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5"},
		"high":     {"anthropic/claude-opus-5"},
	})

	svc.handleStruggleEscalation(context.Background(), uuid.New(), struggleTestKey(3), "default",
		[]spiralReason{spiralReasonPingPong})

	assert.Empty(t, pins.upserts, "evidence must not repin until the arming flag is on")
	assert.Empty(t, events.events)
}

func TestHandleStruggleEscalation_EvidenceArmsBeforeTheThresholds(t *testing.T) {
	pins := newStubPinStore()
	pins.getFound = true
	pins.getPin = quietPin("claude-haiku-4-5", "balanced")
	events := &recordingStruggleStore{}
	svc := newStruggleEscalationSvc(pins, events, map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5"},
		"high":     {"anthropic/claude-opus-5"},
	}).WithStruggleEvidenceArming(true)

	svc.handleStruggleEscalation(context.Background(), uuid.New(), struggleTestKey(4), "default",
		[]spiralReason{spiralReasonPingPong, spiralReasonNoProgress})

	require.Len(t, pins.upserts, 1)
	assert.Equal(t, "claude-opus-5", pins.upserts[0].Model)

	require.Len(t, events.events, 1)
	assert.Equal(t, struggleArmingEvidence, events.events[0].ArmingMode)
	assert.Equal(t, []string{string(spiralReasonPingPong), string(spiralReasonNoProgress)}, events.events[0].EvidenceReasons)
}

func TestHandleStruggleEscalation_EvidenceStaysOffTheOpeningTurns(t *testing.T) {
	pins := newStubPinStore()
	pins.getFound = true
	pin := quietPin("claude-haiku-4-5", "balanced")
	pin.TurnCount = struggleEvidenceMinTurns - 2 // +1 in-flight still short
	pins.getPin = pin
	events := &recordingStruggleStore{}
	svc := newStruggleEscalationSvc(pins, events, map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5"},
		"high":     {"anthropic/claude-opus-5"},
	}).WithStruggleEvidenceArming(true)

	svc.handleStruggleEscalation(context.Background(), uuid.New(), struggleTestKey(5), "default",
		[]spiralReason{spiralReasonPingPong})

	assert.Empty(t, pins.upserts, "an imported history must not repin on turn one")
	assert.Empty(t, events.events)
}

func TestHandleStruggleEscalation_EvidenceCannotArmALateSession(t *testing.T) {
	pins := newStubPinStore()
	pins.getFound = true
	pin := strugglingPin("claude-haiku-4-5", "balanced")
	pin.TurnCount = struggleLateTurns
	pin.FirstPinnedAt = time.Now().Add(-struggleLateWall - time.Minute)
	pins.getPin = pin
	events := &recordingStruggleStore{}
	svc := newStruggleEscalationSvc(pins, events, map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5"},
		"high":     {"anthropic/claude-opus-5"},
	}).WithStruggleEvidenceArming(true)

	svc.handleStruggleEscalation(context.Background(), uuid.New(), struggleTestKey(7), "default",
		[]spiralReason{spiralReasonPingPong})

	assert.Empty(t, pins.upserts, "late is deliberately unarmed; evidence must not smuggle it in")
	assert.Empty(t, events.events)
}

func TestHandleStruggleEscalation_TimerArmingKeepsItsAttribution(t *testing.T) {
	pins := newStubPinStore()
	pins.getFound = true
	pins.getPin = strugglingPin("claude-haiku-4-5", "balanced")
	events := &recordingStruggleStore{}
	svc := newStruggleEscalationSvc(pins, events, map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5"},
		"high":     {"anthropic/claude-opus-5"},
	}).WithStruggleEvidenceArming(true)

	svc.handleStruggleEscalation(context.Background(), uuid.New(), struggleTestKey(6), "default",
		[]spiralReason{spiralReasonErrStreak})

	require.Len(t, events.events, 1)
	assert.Equal(t, struggleArmingTurnWall, events.events[0].ArmingMode,
		"past the thresholds the timer owns the fire, evidence or not")
}

// Regression: the escalation target used to be chosen without consulting the
// installation's excluded models, pinning an arm that every later turn dropped
// as excluded.
func TestHandleStruggleEscalation_SkipsInstallationExcludedTargets(t *testing.T) {
	pins := newStubPinStore()
	pins.getFound = true
	pins.getPin = strugglingPin("claude-haiku-4-5", "balanced")
	events := &recordingStruggleStore{}
	svc := newStruggleEscalationSvc(pins, events, map[string][]string{
		"balanced": {"anthropic/claude-haiku-4.5", "anthropic/claude-sonnet-4-5"},
		"high":     {"z-ai/glm-5.3-flash", "anthropic/claude-opus-5"},
	})
	ctx := context.WithValue(context.Background(), InstallationExcludedModelsContextKey{}, []string{"z-ai/glm-5.3-flash"})

	svc.handleStruggleEscalation(ctx, uuid.New(), struggleTestKey(1), "default", nil)

	require.Len(t, pins.upserts, 1)
	assert.Equal(t, "claude-opus-5", pins.upserts[0].Model,
		"an installation-excluded arm must never become the escalation target")
	assert.Equal(t, "high", pins.upserts[0].PolicyGroup)
}
