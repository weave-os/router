package floor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type escalationTestStore struct {
	sessions      map[[32]byte]escalation.Session
	checkpoints   map[[32]byte]map[[32]byte]escalation.Checkpoint
	continuations map[string]struct {
		scope   [32]byte
		history json.RawMessage
	}
	failCommit bool
}

func newEscalationTestStore() *escalationTestStore {
	return &escalationTestStore{sessions: make(map[[32]byte]escalation.Session), checkpoints: make(map[[32]byte]map[[32]byte]escalation.Checkpoint), continuations: make(map[string]struct {
		scope   [32]byte
		history json.RawMessage
	})}
}

func (s *escalationTestStore) Claim(_ context.Context, scope [32]byte, _, _ string, _ [32]byte) (escalation.Session, bool, error) {
	return s.sessions[scope], true, nil
}

func (s *escalationTestStore) Checkpoint(_ context.Context, scope, boundary [32]byte) (escalation.Checkpoint, bool, error) {
	c, ok := s.checkpoints[scope][boundary]
	return c, ok, nil
}

func (s *escalationTestStore) Commit(_ context.Context, scope, boundary [32]byte, _ string, session escalation.Session, checkpoint escalation.Checkpoint) error {
	if s.failCommit {
		return escalation.ErrLeaseLost
	}
	s.sessions[scope] = session
	if s.checkpoints[scope] == nil {
		s.checkpoints[scope] = make(map[[32]byte]escalation.Checkpoint)
	}
	s.checkpoints[scope][boundary] = checkpoint
	return nil
}

func (s *escalationTestStore) Release(context.Context, [32]byte, string) error { return nil }

func (s *escalationTestStore) SaveOutcome(_ context.Context, scope [32]byte, ordinal int64, outcome escalation.PreviousOutcome) error {
	session := s.sessions[scope]
	if session.Ordinal == ordinal {
		session.PreviousOutcome = &outcome
		s.sessions[scope] = session
	}
	return nil
}

func (s *escalationTestStore) Invalidate(_ context.Context, scope [32]byte, _ [32]byte, _ string) error {
	session := s.sessions[scope]
	session.FeatureState = nil
	session.FeatureTurns = 0
	session.PreviousOutcome = nil
	s.sessions[scope] = session
	return nil
}

func (s *escalationTestStore) SweepExpired(context.Context) error { return nil }

func (s *escalationTestStore) SaveContinuation(_ context.Context, activation [32]byte, responseID string, scope [32]byte, _ int64, history json.RawMessage) error {
	s.continuations[fmt.Sprintf("%x/%s", activation, responseID)] = struct {
		scope   [32]byte
		history json.RawMessage
	}{scope, history}
	return nil
}

func (s *escalationTestStore) Continuation(_ context.Context, activation [32]byte, responseID string) ([32]byte, json.RawMessage, bool, error) {
	c, ok := s.continuations[fmt.Sprintf("%x/%s", activation, responseID)]
	return c.scope, c.history, ok, nil
}

type escalationTestObserver struct {
	requests []escalation.ObserveRequest
	fail     bool
}

func (o *escalationTestObserver) ObserveEscalation(_ context.Context, r escalation.ObserveRequest) (escalation.ObserveResponse, error) {
	o.requests = append(o.requests, r)
	if o.fail {
		return escalation.ObserveResponse{}, errors.New("unavailable")
	}
	resp := escalation.ObserveResponse{State: json.RawMessage(fmt.Sprintf(`{"observed_turns":%d}`, len(o.requests))), ModelID: "test-model", PackageSHA256: fmt.Sprintf("%064d", 1)}
	if r.PredictDue {
		resp.Prediction = &escalation.Prediction{Score: .9, Threshold: .5, Escalate: true}
	}
	return resp, nil
}

func escalationTestContext(active, shadow bool) context.Context {
	return flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyEscalationXGBoostEnabled: active, flags.KeyEscalationXGBoostShadowEnabled: shadow}})
}

func escalationTestEnvelope(t *testing.T, turn int) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"start"},{"role":"assistant","content":"working"},{"role":"user","content":"turn %d"}]}`, turn)))
	require.NoError(t, err)
	return env
}

func TestEscalationCadenceReplayFloorAndGates(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	svc := newTestService().WithEscalation(store, observer)
	ctx := escalationTestContext(true, false)
	res := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	req := router.Request{}
	for n := 1; n <= 6; n++ {
		turn := svc.beginEscalation(ctx, escalationTestEnvelope(t, n), req, &res)
		require.NotNil(t, turn)
		require.Equal(t, escalation.ModeActive, turn.session.Mode)
		require.NotNil(t, turn.session.Epoch)
		require.Zero(t, *turn.session.Epoch)
		constraint := turn.constraint()
		switch n {
		case 5:
			require.True(t, constraint.Escalate)
			res.Decision.Metadata = &router.RoutingMetadata{Escalation: &escalation.Decision{Baseline: escalation.Low, Effective: escalation.Maximum, Outcome: escalation.OutcomePromoted, Constrained: true}}
		case 6:
			require.Equal(t, escalation.Maximum, constraint.Floor)
			require.False(t, constraint.Escalate)
		}
		require.NoError(t, svc.finishEscalation(ctx, turn, &res))
	}
	require.Len(t, observer.requests, 6)
	for i, r := range observer.requests {
		require.Equal(t, i == 4, r.PredictDue)
	}
	replay := svc.beginEscalation(ctx, escalationTestEnvelope(t, 5), req, &res)
	require.True(t, replay.replay)
	require.False(t, replay.constraint().Escalate)
	require.NoError(t, svc.finishEscalation(ctx, replay, &res))
	require.Len(t, observer.requests, 6)
	require.Nil(t, svc.beginEscalation(escalationTestContext(false, false), escalationTestEnvelope(t, 7), req, &res))
	shadow := svc.beginEscalation(escalationTestContext(false, true), escalationTestEnvelope(t, 7), req, &res)
	require.NotNil(t, shadow)
	require.Equal(t, escalation.ModeShadow, shadow.session.Mode)
	require.NotNil(t, shadow.session.Epoch)
	require.Zero(t, *shadow.session.Epoch)
	require.Nil(t, shadow.constraint())
	require.Empty(t, shadow.session.Floor)
	forced := req
	forced.ForceModel = "claude-opus-4-8"
	require.Nil(t, svc.beginEscalation(ctx, escalationTestEnvelope(t, 7), forced, &res))
}

func TestEscalationObservationFailureResetsFeaturesRetainsFloor(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	svc := newTestService().WithEscalation(store, observer)
	ctx := escalationTestContext(true, false)
	res := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	turn := svc.beginEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &res)
	turn.session.Floor = escalation.High
	require.NoError(t, svc.finishEscalation(ctx, turn, &res))
	observer.fail = true
	turn = svc.beginEscalation(ctx, escalationTestEnvelope(t, 2), router.Request{}, &res)
	require.Equal(t, int64(2), turn.session.Ordinal)
	require.Zero(t, turn.session.FeatureTurns)
	require.Equal(t, escalation.High, turn.constraint().Floor)
	require.NoError(t, svc.finishEscalation(ctx, turn, &res))
	observer.fail = false
	observer.requests = nil
	for n := 3; n <= 10; n++ {
		turn = svc.beginEscalation(ctx, escalationTestEnvelope(t, n), router.Request{}, &res)
		require.NoError(t, svc.finishEscalation(ctx, turn, &res))
	}
	require.True(t, observer.requests[4].PredictDue)
	require.False(t, observer.requests[7].PredictDue)
}

func TestEscalationResponsesContinuation(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	svc := newTestService().WithEscalation(store, observer)
	ctx := escalationTestContext(true, false)
	res := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	turn := svc.beginEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &res)
	require.NoError(t, svc.finishEscalation(ctx, turn, &res))
	capture := newCaptureWriter()
	_, err := capture.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"completed"}]}]}`))
	require.NoError(t, err)
	svc.recordEscalationOutcome(ctx, res, nil, capture, translate.EscalationResponseResponses)
	ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, []byte(`{"model":"weave","previous_response_id":"resp_1","input":"continue"}`))
	res.SessionKey[0] = 42
	continued := svc.beginEscalation(ctx, escalationTestEnvelope(t, 2), router.Request{}, &res)
	require.NotNil(t, continued)
	require.Equal(t, turn.scope, continued.scope)
	require.Equal(t, int64(2), continued.session.Ordinal)
	var observed translate.EscalationObservation
	require.NoError(t, json.Unmarshal(observer.requests[1].Observation, &observed))
	require.True(t, observed.HistoryComplete)
	require.Len(t, observed.Messages, 5)
	otherOrg := res
	otherOrg.InstallationID = uuid.New()
	require.Nil(t, svc.beginEscalation(ctx, escalationTestEnvelope(t, 2), router.Request{}, &otherOrg))
}

func TestEscalationResponsesContinuationSupportsLegacyActivation(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	svc := newTestService().WithEscalation(store, observer)
	ctx := escalationTestContext(true, false)
	res := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	turn := svc.beginEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &res)
	require.NoError(t, svc.finishEscalation(ctx, turn, &res))
	legacyActivation := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%d", res.InstallationID, "test-key", res.Strategy, escalation.ModeActive, 0)))
	store.continuations[fmt.Sprintf("%x/%s", legacyActivation, "resp_legacy")] = struct {
		scope   [32]byte
		history json.RawMessage
	}{scope: turn.scope, history: json.RawMessage(`[{"role":"user","blocks":[{"type":"text","text":"start"}]}]`)}

	ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, []byte(`{"model":"weave","previous_response_id":"resp_legacy","input":"continue"}`))
	continued := svc.beginEscalation(ctx, escalationTestEnvelope(t, 2), router.Request{}, &res)
	require.NotNil(t, continued)
	require.Equal(t, turn.scope, continued.scope)
}

func TestEscalationOrdinaryHigherClassificationDoesNotRaiseFloor(t *testing.T) {
	store := newEscalationTestStore()
	svc := newTestService().WithEscalation(store, &escalationTestObserver{})
	res := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	turn := svc.beginEscalation(escalationTestContext(true, false), escalationTestEnvelope(t, 1), router.Request{}, &res)
	turn.session.Floor = escalation.Medium
	res.Decision.Metadata = &router.RoutingMetadata{Escalation: &escalation.Decision{Baseline: escalation.High, Effective: escalation.High, Outcome: escalation.OutcomeFloor, Constrained: true}}
	require.NoError(t, svc.finishEscalation(context.Background(), turn, &res))
	require.Equal(t, escalation.Medium, store.sessions[turn.scope].Floor)
}

func TestEscalationShadowMarkerRequiresCommittedPositiveShadowCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name       string
		showMarker bool
		active     bool
		prediction *escalation.Prediction
		failCommit bool
		wantMarked bool
	}{
		{name: "hidden positive shadow", prediction: &escalation.Prediction{Escalate: true}},
		{name: "positive shadow", showMarker: true, prediction: &escalation.Prediction{Escalate: true}, wantMarked: true},
		{name: "active promotion", showMarker: true, active: true, prediction: &escalation.Prediction{Escalate: true}},
		{name: "negative shadow", showMarker: true, prediction: &escalation.Prediction{}},
		{name: "between checkpoints", showMarker: true},
		{name: "uncommitted shadow", showMarker: true, prediction: &escalation.Prediction{Escalate: true}, failCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newEscalationTestStore()
			store.failCommit = tc.failCommit
			svc := newTestService().WithEscalation(store, nil)
			turn := &xgbTurn{active: tc.active, checkpoint: escalation.Checkpoint{Ordinal: 5, Prediction: tc.prediction}}
			var routed testResult
			ctx := flags.WithOverrides(context.Background(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyEscalationXGBoostShadowMarkerEnabled: tc.showMarker}})
			err := svc.finishEscalation(ctx, turn, &routed)
			if tc.failCommit {
				require.ErrorIs(t, err, escalation.ErrLeaseLost)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantMarked, routed.EscalationShadowMarked)
		})
	}
}

func TestActiveEscalationRemainsRestrictedToEmbeddingHMM(t *testing.T) {
	for _, strategy := range []router.Strategy{router.Strategy("cluster"), router.StrategyRetiredRL, router.StrategyHMM, router.StrategyHMMBeta} {
		t.Run(string(strategy), func(t *testing.T) {
			observer := &escalationTestObserver{}
			svc := newTestService().WithEscalation(newEscalationTestStore(), observer)
			res := testResult{Strategy: strategy, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
			for _, shadow := range []bool{false, true} {
				require.Nil(t, svc.beginEscalation(escalationTestContext(true, shadow), escalationTestEnvelope(t, 1), router.Request{}, &res))
			}
			require.Empty(t, observer.requests)
		})
	}
}
