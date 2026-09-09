package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
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
	svc := (&Service{}).WithEscalation(store, observer)
	ctx := escalationTestContext(true, false)
	res := turnLoopResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	req := router.Request{}
	for n := 1; n <= 6; n++ {
		turn := svc.beginEscalation(ctx, escalationTestEnvelope(t, n), req, &res, "test-key")
		require.NotNil(t, turn)
		constraint := turn.constraint()
		if n == 5 {
			require.True(t, constraint.Escalate)
			res.Decision.Metadata = &router.RoutingMetadata{Escalation: &escalation.Decision{Baseline: escalation.Low, Effective: escalation.Medium, Outcome: escalation.OutcomePromoted, Constrained: true}}
		} else if n == 6 {
			require.Equal(t, escalation.Medium, constraint.Floor)
			require.False(t, constraint.Escalate)
		}
		require.NoError(t, svc.finishEscalation(ctx, turn, &res, nil))
	}
	require.Len(t, observer.requests, 6)
	for i, r := range observer.requests {
		require.Equal(t, i == 4, r.PredictDue)
	}
	replay := svc.beginEscalation(ctx, escalationTestEnvelope(t, 5), req, &res, "test-key")
	require.True(t, replay.replay)
	require.False(t, replay.constraint().Escalate)
	require.NoError(t, svc.finishEscalation(ctx, replay, &res, nil))
	require.Len(t, observer.requests, 6)
	require.Nil(t, svc.beginEscalation(escalationTestContext(false, false), escalationTestEnvelope(t, 7), req, &res, "test-key"))
	shadow := svc.beginEscalation(escalationTestContext(false, true), escalationTestEnvelope(t, 7), req, &res, "test-key")
	require.NotNil(t, shadow)
	require.Nil(t, shadow.constraint())
	require.Empty(t, shadow.session.Floor)
	forced := req
	forced.ForceModel = "claude-opus-4-8"
	require.Nil(t, svc.beginEscalation(ctx, escalationTestEnvelope(t, 7), forced, &res, "test-key"))
}
func TestEscalationObservationFailureResetsFeaturesRetainsFloor(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	svc := (&Service{}).WithEscalation(store, observer)
	ctx := escalationTestContext(true, false)
	res := turnLoopResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	turn := svc.beginEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &res, "test-key")
	turn.session.Floor = escalation.High
	require.NoError(t, svc.finishEscalation(ctx, turn, &res, nil))
	observer.fail = true
	turn = svc.beginEscalation(ctx, escalationTestEnvelope(t, 2), router.Request{}, &res, "test-key")
	require.Equal(t, int64(2), turn.session.Ordinal)
	require.Zero(t, turn.session.FeatureTurns)
	require.Equal(t, escalation.High, turn.constraint().Floor)
	require.NoError(t, svc.finishEscalation(ctx, turn, &res, nil))
	observer.fail = false
	observer.requests = nil
	for n := 3; n <= 10; n++ {
		turn = svc.beginEscalation(ctx, escalationTestEnvelope(t, n), router.Request{}, &res, "test-key")
		require.NoError(t, svc.finishEscalation(ctx, turn, &res, nil))
	}
	require.True(t, observer.requests[4].PredictDue)
	require.False(t, observer.requests[7].PredictDue)
}
func TestEscalationResponsesContinuation(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	svc := (&Service{}).WithEscalation(store, observer)
	ctx := escalationTestContext(true, false)
	res := turnLoopResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	turn := svc.beginEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &res, "test-key")
	require.NoError(t, svc.finishEscalation(ctx, turn, &res, nil))
	capture := newCaptureWriter(httptest.NewRecorder(), escalationHistoryMaxBytes)
	_, err := capture.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"completed"}]}]}`))
	require.NoError(t, err)
	svc.recordEscalationOutcome(ctx, res, nil, capture, translate.EscalationResponseResponses)
	ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, []byte(`{"model":"weave","previous_response_id":"resp_1","input":"continue"}`))
	res.SessionKey[0] = 42
	continued := svc.beginEscalation(ctx, escalationTestEnvelope(t, 2), router.Request{}, &res, "test-key")
	require.NotNil(t, continued)
	require.Equal(t, turn.scope, continued.scope)
	require.Equal(t, int64(2), continued.session.Ordinal)
	var observed translate.EscalationObservation
	require.NoError(t, json.Unmarshal(observer.requests[1].Observation, &observed))
	require.True(t, observed.HistoryComplete)
	require.Len(t, observed.Messages, 5)
	otherOrg := res
	otherOrg.InstallationID = uuid.New()
	require.Nil(t, svc.beginEscalation(ctx, escalationTestEnvelope(t, 2), router.Request{}, &otherOrg, "test-key"))
}

// The policy implementation's eligibility behavior is tested in policy; this
// double exposes the orchestration's actual classification intent at dispatch.
type escalationDispatchRouter struct{}

func (escalationDispatchRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	group := escalation.Low
	var intervention *escalation.Decision
	if req.Escalation != nil {
		group = escalation.Higher(group, req.Escalation.Floor)
		if req.Escalation.Escalate {
			group = escalation.Next(group)
		}
		outcome := escalation.OutcomeFloor
		if req.Escalation.Escalate {
			outcome = escalation.OutcomePromoted
		}
		intervention = &escalation.Decision{Baseline: escalation.Low, Effective: group, Outcome: outcome, Constrained: true}
	}
	models := map[escalation.Group]string{escalation.Low: "claude-haiku-4-5", escalation.Medium: "claude-sonnet-4-6", escalation.High: "claude-opus-4-7", escalation.Maximum: "claude-opus-4-8"}
	return router.Decision{Model: models[group], Provider: providers.ProviderAnthropic, Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMMEmbedding), Escalation: intervention}}, nil
}
func TestEscalationLiveModelThroughTurnLoop(t *testing.T) {
	endpoint := os.Getenv("ESCALATION_TEST_URL")
	fixturePath := os.Getenv("ESCALATION_TEST_FIXTURE")
	if endpoint == "" || fixturePath == "" {
		t.Skip("live packaged classifier integration")
	}
	fixture, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	var observations []json.RawMessage
	require.NoError(t, json.Unmarshal(fixture, &observations))
	require.GreaterOrEqual(t, len(observations), 11)
	store := newEscalationTestStore()
	client := policyclient.New(endpoint, &http.Client{}, time.Second)
	ctx := escalationTestContext(true, false)
	installation := uuid.New()
	var last turnLoopResult
	for n, body := range observations[:11] {
		// Recreate the service each turn: all continuity must come from the store.
		svc := NewService(nil, nil, nil, false, nil, newStubPinStore(), false, providers.ProviderAnthropic, "claude-opus-4-8", nil).WithEscalation(store, client).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: escalationDispatchRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: true}})
		env, parseErr := translate.ParseAnthropic(body)
		require.NoError(t, parseErr)
		features := env.RoutingFeatures(false)
		last, err = svc.runTurnLoop(ctx, env, features, "test-key", installation, "", http.Header{}, router.Request{RequestedModel: features.Model, EstimatedInputTokens: features.Tokens, HasTools: features.HasTools, ConversationMessages: conversationMessagesForRouting(env)})
		require.NoError(t, err)
		require.Equal(t, int64(n+1), last.EscalationOrdinal)
		if n < 9 {
			require.Equal(t, "claude-haiku-4-5", last.Decision.Model)
		} else {
			require.Equal(t, "claude-sonnet-4-6", last.Decision.Model)
		}
	}
	require.Equal(t, escalation.Medium, store.sessions[last.EscalationScope].Floor)
}

func TestEscalationOrdinaryHigherClassificationDoesNotRaiseFloor(t *testing.T) {
	store := newEscalationTestStore()
	svc := (&Service{}).WithEscalation(store, &escalationTestObserver{})
	res := turnLoopResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	turn := svc.beginEscalation(escalationTestContext(true, false), escalationTestEnvelope(t, 1), router.Request{}, &res, "test-key")
	turn.session.Floor = escalation.Medium
	res.Decision.Metadata = &router.RoutingMetadata{Escalation: &escalation.Decision{Baseline: escalation.High, Effective: escalation.High, Outcome: escalation.OutcomeFloor, Constrained: true}}
	require.NoError(t, svc.finishEscalation(context.Background(), turn, &res, nil))
	require.Equal(t, escalation.Medium, store.sessions[turn.scope].Floor)
}
func TestEscalationCommitFailureDoesNotDispatchUncommittedPromotion(t *testing.T) {
	store := newEscalationTestStore()
	svc := NewService(nil, nil, nil, false, nil, newStubPinStore(), false, providers.ProviderAnthropic, "claude-opus-4-8", nil).WithEscalation(store, &escalationTestObserver{}).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: escalationDispatchRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: true}})
	ctx := escalationTestContext(true, false)
	installation := uuid.New()
	for n := 1; n <= 5; n++ {
		store.failCommit = n == 5
		env := escalationTestEnvelope(t, n)
		feats := env.RoutingFeatures(false)
		res, err := svc.runTurnLoop(ctx, env, feats, "test-key", installation, "", http.Header{}, router.Request{RequestedModel: feats.Model})
		require.NoError(t, err)
		require.Equal(t, "claude-haiku-4-5", res.Decision.Model)
		if n == 5 {
			require.Zero(t, res.EscalationOrdinal)
			require.Equal(t, "claude-haiku-4-5", res.Fresh.Model)
		}
	}
}

func TestEscalationRecordsServedHistoryWithoutReplacingBaselinePin(t *testing.T) {
	pins := newStubPinStore()
	svc := NewService(nil, nil, nil, false, nil, pins, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	res := turnLoopResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), PinRole: "default", Decision: router.Decision{Model: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic, Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMMEmbedding), Escalation: &escalation.Decision{Baseline: escalation.Low, Effective: escalation.Medium, Outcome: escalation.OutcomePromoted, Constrained: true}}}}
	res.SessionKey[0] = 1
	svc.recordTurnUsage(context.Background(), res, providers.ProviderAnthropic, "claude-sonnet-4-6", 100, 20, 0, 0)
	require.Len(t, pins.upserts, 1)
	require.Equal(t, hmmHistoryRole(res.PinRole), pins.upserts[0].Role)
	require.Equal(t, "claude-sonnet-4-6", pins.lastUsage.ServedModel)
	require.Equal(t, 20, pins.lastUsage.OutputTokens)
}

func TestEscalationCommitFailurePreservesOrdinaryStickySelection(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	pins := &rolePinStore{byRole: map[string]sessionpin.Pin{
		roleForTier(catalog.TierFor("claude-opus-4-8")): {Provider: providers.ProviderAnthropic, Model: "claude-opus-4-7", Strategy: router.StrategyHMMEmbedding, PinnedUntil: time.Now().Add(time.Hour)},
	}}
	svc := NewService(nil, nil, nil, false, nil, pins, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).WithEscalation(store, observer).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: escalationDispatchRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1}})
	ctx := flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyEscalationXGBoostEnabled: true, flags.KeyPlannerEnabled: false}})
	installation := uuid.New()
	for n := 1; n <= 5; n++ {
		store.failCommit = n == 5
		env := escalationTestEnvelope(t, n)
		feats := env.RoutingFeatures(false)
		res, err := svc.runTurnLoop(ctx, env, feats, "test-key", installation, "", http.Header{}, router.Request{RequestedModel: feats.Model})
		require.NoError(t, err)
		require.Equal(t, "claude-opus-4-7", res.Decision.Model)
		require.True(t, res.StickyHit)
		if n == 5 {
			require.Zero(t, res.EscalationOrdinal)
			require.Nil(t, res.Decision.Metadata)
		}
	}
	require.Len(t, observer.requests, 5, "fail-open selection must not observe the same turn twice")
}

func TestEscalationCommitFailureDoesNotRepeatUnconstrainedSelection(t *testing.T) {
	for _, outcome := range []escalation.Outcome{escalation.OutcomeMaximum, escalation.OutcomeNoTarget} {
		t.Run(string(outcome), func(t *testing.T) {
			store := newEscalationTestStore()
			store.failCommit = true
			pins := newStubPinStore()
			classifier := &authoritativeTestRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMMEmbedding), Escalation: &escalation.Decision{Baseline: escalation.Maximum, Effective: escalation.Maximum, Outcome: outcome}}}}
			svc := NewService(nil, nil, nil, false, nil, pins, false, providers.ProviderAnthropic, "claude-opus-4-8", nil).WithEscalation(store, &escalationTestObserver{}).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: classifier, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: true}})
			env := escalationTestEnvelope(t, 1)
			feats := env.RoutingFeatures(false)
			res, err := svc.runTurnLoop(escalationTestContext(true, false), env, feats, "test-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: feats.Model})
			require.NoError(t, err)
			require.Equal(t, "claude-opus-4-8", res.Decision.Model)
			require.Zero(t, res.EscalationOrdinal)
			require.Len(t, classifier.requests, 1, "failed observational commits cannot repeat ordinary routing and its side effects")
		})
	}
}
