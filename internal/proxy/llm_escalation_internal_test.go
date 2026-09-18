package proxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type llmEscalationStoreStub struct {
	mu              sync.Mutex
	completion      llmescalation.Completion
	finished        chan struct{}
	judgment        llmescalation.Judgment
	failure         llmescalation.FailureCode
	applyCalls      int
	noTargetCalls   int
	completeCalls   int
	continuationErr error
}

func (s *llmEscalationStoreStub) Start(context.Context, llmescalation.StartRequest) (llmescalation.Session, error) {
	return llmescalation.Session{}, nil
}
func (s *llmEscalationStoreStub) Complete(context.Context, llmescalation.CompleteRequest) (llmescalation.Completion, error) {
	s.completeCalls++
	return s.completion, nil
}
func (s *llmEscalationStoreStub) FinishJob(_ context.Context, _ llmescalation.Job, judgment llmescalation.Judgment, failure llmescalation.FailureCode) error {
	s.mu.Lock()
	s.judgment, s.failure = judgment, failure
	s.mu.Unlock()
	close(s.finished)
	return nil
}
func (s *llmEscalationStoreStub) Apply(context.Context, llmescalation.ApplyRequest) (bool, error) {
	s.applyCalls++
	return true, nil
}
func (s *llmEscalationStoreStub) RecordNoTarget(context.Context, llmescalation.ApplyRequest) error {
	s.noTargetCalls++
	return nil
}
func (s *llmEscalationStoreStub) SaveContinuation(context.Context, llmescalation.ContinuationRequest) error {
	return s.continuationErr
}
func (s *llmEscalationStoreStub) Continuation(context.Context, [32]byte, string) (llmescalation.Continuation, bool, error) {
	return llmescalation.Continuation{}, false, nil
}
func (s *llmEscalationStoreStub) ListJobs(context.Context, string, int) ([]llmescalation.Job, error) {
	return nil, nil
}
func (s *llmEscalationStoreStub) GetJob(context.Context, string, string) (llmescalation.Job, bool, error) {
	return llmescalation.Job{}, false, nil
}
func (s *llmEscalationStoreStub) ListSessions(context.Context, string, int32, int32) ([]llmescalation.Session, error) {
	return nil, nil
}
func (s *llmEscalationStoreStub) Summary(context.Context, string) (llmescalation.Summary, error) {
	return llmescalation.Summary{}, nil
}
func (s *llmEscalationStoreStub) GetSession(context.Context, string, [32]byte) (llmescalation.Session, []llmescalation.Job, bool, error) {
	return llmescalation.Session{}, nil, false, nil
}
func (s *llmEscalationStoreStub) SweepExpired(context.Context) error { return nil }

type blockingEscalationJudge struct {
	started  chan llmescalation.JudgeRequest
	release  chan struct{}
	judgment llmescalation.Judgment
}

type escalationConfigurationStub struct {
	selection llmescalation.Selection
	updates   int
}

func (s *escalationConfigurationStub) GetSelection(context.Context, string) (llmescalation.Selection, error) {
	return s.selection, nil
}
func (s *escalationConfigurationStub) SetSelection(_ context.Context, _ string, update llmescalation.SelectionUpdate) (llmescalation.Selection, error) {
	s.updates++
	s.selection.Active, s.selection.Shadow, s.selection.Cadence = update.Active, update.Shadow, update.Cadence
	return s.selection, nil
}

func (j *blockingEscalationJudge) Judge(_ context.Context, request llmescalation.JudgeRequest) (llmescalation.Judgment, error) {
	j.started <- request
	<-j.release
	return j.judgment, nil
}

func TestLLMEscalationJudgeRunsAfterCompletionWithoutBlocking(t *testing.T) {
	job := llmescalation.Job{ID: "job-1", Lifetime: "life-1", Checkpoint: 3, Model: policy.EscalationJudgeModel, Provider: providers.ProviderFireworks}
	store := &llmEscalationStoreStub{completion: llmescalation.Completion{Job: &job}, finished: make(chan struct{})}
	judge := &blockingEscalationJudge{started: make(chan llmescalation.JudgeRequest, 1), release: make(chan struct{}), judgment: llmescalation.Judgment{Escalate: true, Reason: "loop"}}
	service := (&Service{}).WithLLMEscalation(store, judge)
	turn := &llmEscalationTurn{
		requestID:   "request-3",
		observation: translate.EscalationObservation{Messages: []translate.EscalationMessage{{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "fix it"}}}}},
		session:     llmescalation.Session{Lifetime: "life-1", InstallationID: "00000000-0000-0000-0000-000000000001", Config: llmescalation.Config{Cadence: 3}},
	}
	capture := newCaptureWriter(httptest.NewRecorder(), escalationHistoryMaxBytes)
	_, err := capture.Write([]byte(`{"id":"response","content":[{"type":"text","text":"trying again"}],"stop_reason":"end_turn"}`))
	require.NoError(t, err)

	startedAt := time.Now()
	service.completeLLMEscalation(context.Background(), turnLoopResult{llmEscalation: turn}, nil, capture, translate.EscalationResponseAnthropic)
	require.Less(t, time.Since(startedAt), 100*time.Millisecond)
	request := <-judge.started
	require.Contains(t, request.Transcript, "[assistant] trying again")
	close(judge.release)
	select {
	case <-store.finished:
	case <-time.After(time.Second):
		t.Fatal("judge result was not persisted")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	require.True(t, store.judgment.Escalate)
	require.Empty(t, store.judgment.Reason, "rationale is content and capture is disabled")
	require.Equal(t, llmescalation.FailureNone, store.failure)
}

func TestLLMEscalationApplicationSeparatesActiveAndShadow(t *testing.T) {
	positive := &llmescalation.Job{ID: "job", Judgment: &llmescalation.Judgment{Escalate: true}}
	store := &llmEscalationStoreStub{}
	service := (&Service{}).WithLLMEscalation(store, &blockingEscalationJudge{})
	shadow := &llmEscalationTurn{session: llmescalation.Session{Pending: positive}}
	require.NoError(t, service.applyLLMEscalation(context.Background(), shadow, router.Decision{}))
	require.Equal(t, 1, store.applyCalls)

	active := &llmEscalationTurn{active: true, session: llmescalation.Session{Pending: positive}}
	require.NoError(t, service.applyLLMEscalation(context.Background(), active, router.Decision{}))
	require.Equal(t, 1, store.noTargetCalls)
	require.Equal(t, 1, store.applyCalls)

	active.session.Floor = escalation.Maximum
	require.NoError(t, service.applyLLMEscalation(context.Background(), active, router.Decision{}))
	require.Equal(t, 1, store.noTargetCalls)
}

func TestLLMEscalationConfigurationGatesActiveRollout(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Epoch: 2}}
	invalidations := 0
	service := (&Service{}).
		WithLLMEscalation(&llmEscalationStoreStub{}, &blockingEscalationJudge{}).
		WithEscalationConfiguration(configuration, func(string) { invalidations++ }, false)

	_, err := service.UpdateEscalationSelection(context.Background(), "installation", llmescalation.SelectionUpdate{Active: flags.EscalationClassifierSwitchyard, Cadence: 3, Epoch: 2})
	require.ErrorIs(t, err, ErrEscalationJudgeUnavailable)
	require.Zero(t, configuration.updates)

	selection, err := service.UpdateEscalationSelection(context.Background(), "installation", llmescalation.SelectionUpdate{Active: flags.EscalationClassifierXGB, Shadow: flags.EscalationClassifierSwitchyard, Cadence: 3, Epoch: 2})
	require.NoError(t, err)
	require.Equal(t, flags.EscalationClassifierSwitchyard, selection.Shadow)
	require.Equal(t, 1, configuration.updates)
	require.Equal(t, 1, invalidations)
}

func TestLLMEscalationActiveRolloutGateAppliesToRequestOverrides(t *testing.T) {
	overrides := flags.Overrides{Strings: map[flags.Key]string{
		flags.KeyEscalationActiveClassifier: string(flags.EscalationClassifierSwitchyard),
	}}
	ctx := flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), overrides)
	service := (&Service{}).
		WithLLMEscalation(&llmEscalationStoreStub{}, &blockingEscalationJudge{}).
		WithEscalationConfiguration(nil, nil, false)
	requestResult := turnLoopResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}

	rejected := service.beginLLMEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &requestResult, "test-key")
	require.Nil(t, rejected)

	service.llmEscalationActiveEnabled = true
	accepted := service.beginLLMEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &requestResult, "test-key")
	require.NotNil(t, accepted)
	require.True(t, accepted.active)
}

func TestLLMEscalationContinuationFailureStillCompletesTurn(t *testing.T) {
	store := &llmEscalationStoreStub{continuationErr: errors.New("continuation unavailable")}
	service := (&Service{}).WithLLMEscalation(store, &blockingEscalationJudge{})
	turn := &llmEscalationTurn{session: llmescalation.Session{Config: llmescalation.Config{Cadence: 3}}}
	capture := newCaptureWriter(httptest.NewRecorder(), escalationHistoryMaxBytes)
	_, err := capture.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`))
	require.NoError(t, err)

	service.completeLLMEscalation(context.Background(), turnLoopResult{llmEscalation: turn}, nil, capture, translate.EscalationResponseResponses)
	require.Equal(t, 1, store.completeCalls)
}

func TestLLMEscalationFailureRecognizesCanceledDeadline(t *testing.T) {
	require.Equal(t, llmescalation.FailureTimeout, llmEscalationFailure(context.Canceled, context.DeadlineExceeded))
	require.Equal(t, llmescalation.FailureJudge, llmEscalationFailure(context.Canceled, nil))
	require.Equal(t, llmescalation.FailureInvalid, llmEscalationFailure(ErrInvalidEscalationJudgment, nil))
}

var _ llmescalation.Store = (*llmEscalationStoreStub)(nil)
var _ llmescalation.Judge = (*blockingEscalationJudge)(nil)
