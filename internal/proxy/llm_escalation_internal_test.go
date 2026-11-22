package proxy

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/llmescalation"
)

type llmEscalationStoreStub struct {
	mu                sync.Mutex
	completion        llmescalation.Completion
	finished          chan struct{}
	finishedJob       llmescalation.Job
	judgment          llmescalation.Judgment
	failure           llmescalation.FailureCode
	applyCalls        int
	noTargetCalls     int
	completeCalls     int
	completionRequest llmescalation.CompleteRequest
	continuationErr   error
}

func (s *llmEscalationStoreStub) Start(context.Context, llmescalation.StartRequest) (llmescalation.Session, error) {
	return llmescalation.Session{}, nil
}

func (s *llmEscalationStoreStub) Complete(_ context.Context, request llmescalation.CompleteRequest) (llmescalation.Completion, error) {
	s.completeCalls++
	s.completionRequest = request
	return s.completion, nil
}

func (s *llmEscalationStoreStub) FinishJob(_ context.Context, job llmescalation.Job, judgment llmescalation.Judgment, failure llmescalation.FailureCode) error {
	s.mu.Lock()
	s.finishedJob, s.judgment, s.failure = job, judgment, failure
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

func TestLLMEscalationConfigurationGatesActiveRollout(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Epoch: 2}}
	invalidations := 0
	service := (&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}).
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

func TestQwenEscalationSelectionRequiresDeployedJudge(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Epoch: 2}}
	service := (&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}).
		WithLLMEscalation(&llmEscalationStoreStub{}, &blockingEscalationJudge{}).
		WithEscalationConfiguration(configuration, nil, true)
	update := llmescalation.SelectionUpdate{Active: flags.EscalationClassifierLLM, Cadence: 3, Epoch: 2}

	_, err := service.UpdateEscalationSelection(context.Background(), "installation", update)
	require.ErrorIs(t, err, ErrEscalationJudgeUnavailable)
	require.Zero(t, configuration.updates)

	service.WithQwenEscalation(&blockingEscalationJudge{})
	selection, err := service.UpdateEscalationSelection(context.Background(), "installation", update)
	require.NoError(t, err)
	require.Equal(t, flags.EscalationClassifierLLM, selection.Active)
	require.True(t, selection.Ready)
	require.Equal(t, 1, configuration.updates)
}

func TestSwitchyardEscalationReadinessReflectsDisabledRollout(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Active: flags.EscalationClassifierSwitchyard}}
	service := (&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}).
		WithLLMEscalation(&llmEscalationStoreStub{}, &blockingEscalationJudge{}).
		WithEscalationConfiguration(configuration, nil, false)
	selection, err := service.EscalationSelection(context.Background(), "installation")
	require.NoError(t, err)
	require.False(t, selection.Ready)
	require.Equal(t, "active rollout is disabled", selection.UnavailableReason)
}

var _ llmescalation.Store = (*llmEscalationStoreStub)(nil)

var _ llmescalation.Judge = (*blockingEscalationJudge)(nil)

func TestQwenEscalationSelectionDoesNotRequireSwitchyardActiveRollout(t *testing.T) {
	configuration := &escalationConfigurationStub{selection: llmescalation.Selection{InstallationID: "installation", Epoch: 2}}
	service := (&Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}).
		WithLLMEscalation(&llmEscalationStoreStub{}, nil).
		WithQwenEscalation(&blockingEscalationJudge{}).
		WithEscalationConfiguration(configuration, nil, false)
	selection, err := service.UpdateEscalationSelection(context.Background(), "installation", llmescalation.SelectionUpdate{Active: flags.EscalationClassifierLLM, Cadence: 3, Epoch: 2})
	require.NoError(t, err)
	require.True(t, selection.Ready)
}
