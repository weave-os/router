package floor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

func (j *blockingEscalationJudge) Judge(_ context.Context, request llmescalation.JudgeRequest) (llmescalation.Judgment, error) {
	j.started <- request
	<-j.release
	return j.judgment, nil
}

func TestLLMEscalationJudgeRunsAfterCompletionWithoutBlocking(t *testing.T) {
	job := llmescalation.Job{ID: "job-1", Lifetime: "life-1", Checkpoint: 3, Model: policy.EscalationJudgeModel, Provider: providers.ProviderFireworks}
	store := &llmEscalationStoreStub{completion: llmescalation.Completion{Job: &job}, finished: make(chan struct{})}
	judge := &blockingEscalationJudge{started: make(chan llmescalation.JudgeRequest, 1), release: make(chan struct{}), judgment: llmescalation.Judgment{Escalate: true, Reason: "loop", Release: &llmescalation.ServedRelease{Name: "qwen-escalation-v1.1.0", ModelSHA256: strings.Repeat("a", 64)}}}
	service := newTestService().WithLLMEscalation(store, judge)
	turn := &llmTurn{
		requestID:   "request-3",
		observation: translate.EscalationObservation{Messages: []translate.EscalationMessage{{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "fix it"}}}}},
		session:     llmescalation.Session{Lifetime: "life-1", InstallationID: "00000000-0000-0000-0000-000000000001", Config: llmescalation.Config{Cadence: 3}},
	}
	capture := newCaptureWriter()
	_, err := capture.Write([]byte(`{"id":"response","content":[{"type":"text","text":"trying again"}],"stop_reason":"end_turn"}`))
	require.NoError(t, err)

	startedAt := time.Now()
	service.completeLLMEscalation(context.Background(), testResult{llmEscalation: turn}, capture, translate.EscalationResponseAnthropic)
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
	require.Equal(t, "qwen-escalation-v1.1.0", store.finishedJob.Model, "persistence and telemetry see the release that served the verdict")
	require.Equal(t, strings.Repeat("a", 64), store.finishedJob.ModelSHA256)
}

func TestQwenEscalationUsesFiveTurnIntervalAndModalJudge(t *testing.T) {
	job := llmescalation.Job{ID: "qwen-job", Lifetime: "qwen-life", Checkpoint: 12, Model: "qwen-escalation-v1.0.0", Provider: llmescalation.QwenProvider}
	store := &llmEscalationStoreStub{completion: llmescalation.Completion{Job: &job}, finished: make(chan struct{})}
	switchyard := &blockingEscalationJudge{started: make(chan llmescalation.JudgeRequest, 1), release: make(chan struct{})}
	qwen := &blockingEscalationJudge{started: make(chan llmescalation.JudgeRequest, 1), release: make(chan struct{}), judgment: llmescalation.Judgment{Escalate: true}}
	service := newTestService().WithLLMEscalation(store, switchyard).WithQwenEscalation(qwen)
	messages := make([]translate.EscalationMessage, 0, 23)
	for turnIndex := range 11 {
		messages = append(messages,
			translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: fmt.Sprintf("request %d", turnIndex)}}},
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: fmt.Sprintf("answer %d", turnIndex)}}},
		)
	}
	messages = append(messages, translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "request 11"}}})
	turn := &llmTurn{requestID: "qwen-request", observation: translate.EscalationObservation{Messages: messages}, session: llmescalation.Session{Lifetime: "qwen-life", InstallationID: "00000000-0000-0000-0000-000000000001", CompletedTurns: 11, Config: llmescalation.Config{Cadence: 3, Classifier: flags.EscalationClassifierLLM}}}
	capture := newCaptureWriter()
	_, err := capture.Write([]byte(`{"id":"response","content":[{"type":"text","text":"answer 11"}],"stop_reason":"end_turn"}`))
	require.NoError(t, err)

	service.completeLLMEscalation(context.Background(), testResult{llmEscalation: turn}, capture, translate.EscalationResponseAnthropic)
	select {
	case request := <-qwen.started:
		require.Contains(t, request.Transcript, "The visible interval contains turns 7..11")
		require.Contains(t, request.Transcript, "answer 11")
		require.NotContains(t, request.Transcript, "answer 6")
	case <-time.After(time.Second):
		t.Fatal("Qwen judge did not receive the completed interval")
	}
	select {
	case <-switchyard.started:
		t.Fatal("Switchyard judge received a Qwen job")
	default:
	}
	close(qwen.release)
	select {
	case <-store.finished:
	case <-time.After(time.Second):
		t.Fatal("Qwen verdict was not persisted")
	}
}

func TestLLMEscalationApplicationSeparatesActiveAndShadow(t *testing.T) {
	positive := &llmescalation.Job{ID: "job", Judgment: &llmescalation.Judgment{Escalate: true}}
	store := &llmEscalationStoreStub{}
	service := newTestService().WithLLMEscalation(store, &blockingEscalationJudge{})
	shadow := &llmTurn{session: llmescalation.Session{Pending: positive}}
	require.NoError(t, service.applyLLMEscalation(context.Background(), shadow, router.Decision{}))
	require.Equal(t, 1, store.applyCalls)

	active := &llmTurn{active: true, session: llmescalation.Session{Pending: positive}}
	require.NoError(t, service.applyLLMEscalation(context.Background(), active, router.Decision{}))
	require.Equal(t, 1, store.noTargetCalls)
	require.Equal(t, 1, store.applyCalls)

	active.session.Floor = escalation.Maximum
	require.NoError(t, service.applyLLMEscalation(context.Background(), active, router.Decision{}))
	require.Equal(t, 1, store.noTargetCalls)
}

func TestQwenEscalationDoesNotRequireSwitchyardActiveRollout(t *testing.T) {
	service := newTestService().
		WithLLMEscalation(&llmEscalationStoreStub{}, nil).
		WithQwenEscalation(&blockingEscalationJudge{}).
		WithEscalationConfiguration(nil, nil, false)

	overrides := flags.Overrides{Strings: map[flags.Key]string{
		flags.KeyEscalationActiveClassifier: string(flags.EscalationClassifierLLM),
	}}
	ctx := flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), overrides)
	requestResult := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	turn := service.beginLLMEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &requestResult)
	require.NotNil(t, turn)
	require.True(t, turn.active)
}

func TestQwenEscalationReadsCodexToolOutcomesFromNativeResponses(t *testing.T) {
	service := newTestService().
		WithLLMEscalation(&llmEscalationStoreStub{}, nil).
		WithQwenEscalation(&blockingEscalationJudge{}).
		WithEscalationConfiguration(nil, nil, false)
	overrides := flags.Overrides{Strings: map[flags.Key]string{
		flags.KeyEscalationActiveClassifier: string(flags.EscalationClassifierLLM),
	}}
	native := []byte(`{"model":"gpt-5","input":[{"role":"user","content":"start"}]}`)
	env, err := translate.ParseOpenAI([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"start"}]}`))
	require.NoError(t, err)

	for _, tc := range []struct {
		name  string
		codex bool
	}{
		{name: "codex", codex: true},
		{name: "other client", codex: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), overrides)
			ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, native)
			ctx = context.WithValue(ctx, codexClientContextKey{}, tc.codex)
			requestResult := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
			turn := service.beginLLMEscalation(ctx, env, router.Request{}, &requestResult)
			require.NotNil(t, turn)
			require.Equal(t, tc.codex, turn.observation.CodexToolResults)
		})
	}
}

func TestQwenEscalationSkipsHarnessSideRequests(t *testing.T) {
	service := newTestService().
		WithLLMEscalation(&llmEscalationStoreStub{}, nil).
		WithQwenEscalation(&blockingEscalationJudge{}).
		WithEscalationConfiguration(nil, nil, false)
	overrides := flags.Overrides{Strings: map[flags.Key]string{
		flags.KeyEscalationActiveClassifier: string(flags.EscalationClassifierLLM),
	}}
	suggestion, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":"done"},{"role":"user","content":"[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]"}]}`))
	require.NoError(t, err)
	review, err := translate.ParseOpenAI([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"The following is the Codex agent history"}]}`))
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		env    *translate.RequestEnvelope
		native []byte
	}{
		{name: "claude code prompt suggestion", env: suggestion},
		{name: "codex auto review", env: review, native: []byte(`{"model":"codex-auto-review","input":[{"role":"user","content":"The following is the Codex agent history"}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), overrides)
			if tc.native != nil {
				ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, tc.native)
			}
			requestResult := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
			require.Nil(t, service.beginLLMEscalation(ctx, tc.env, router.Request{}, &requestResult))
		})
	}
}

func TestQwenEscalationSkipsGatewayOnlyInstallation(t *testing.T) {
	service := newTestService().
		WithLLMEscalation(&llmEscalationStoreStub{}, nil).
		WithQwenEscalation(&blockingEscalationJudge{}).
		WithEscalationConfiguration(nil, nil, false)
	overrides := flags.Overrides{Strings: map[flags.Key]string{
		flags.KeyEscalationActiveClassifier: string(flags.EscalationClassifierLLM),
	}}
	ctx := flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), overrides)
	requestResult := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}
	request := router.Request{GatewayProviders: map[string]struct{}{providers.ProviderOpenAIGateway: {}}}
	require.Nil(t, service.beginLLMEscalation(ctx, escalationTestEnvelope(t, 1), request, &requestResult))
}

func TestQwenEscalationRecordsUnavailableIntervalSeparatelyFromCapacity(t *testing.T) {
	store := &llmEscalationStoreStub{}
	service := newTestService().WithLLMEscalation(store, nil).WithQwenEscalation(&blockingEscalationJudge{})
	turn := &llmTurn{
		observation: translate.EscalationObservation{Messages: []translate.EscalationMessage{{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: "continue"}}}}},
		session:     llmescalation.Session{CompletedTurns: 11, Config: llmescalation.Config{Cadence: 3, Classifier: flags.EscalationClassifierLLM}},
	}
	capture := newCaptureWriter()
	_, err := capture.Write([]byte(`{"id":"response","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
	require.NoError(t, err)
	service.completeLLMEscalation(context.Background(), testResult{llmEscalation: turn}, capture, translate.EscalationResponseAnthropic)
	require.Equal(t, 1, store.completeCalls)
	require.Equal(t, llmescalation.FailureIntervalUnavailable, store.completionRequest.IntervalFailure)
	require.False(t, store.completionRequest.Capacity)
}

func TestLLMEscalationSkipsCheckpointsUntilAgentActsOnLatestPrompt(t *testing.T) {
	prompt := func(text string) translate.EscalationMessage {
		return translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: text}}}
	}
	toolResult := translate.EscalationMessage{Role: translate.EscalationRoleUser, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, ContentJSON: `"ok"`}}}
	act := translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolCall, Name: "Bash", ArgumentsJSON: `{"command":"make test"}`}}}
	history := []translate.EscalationMessage{prompt("fix the build"), act}
	for range 10 {
		history = append(history, toolResult, act)
	}
	const toolCallResponse = `{"id":"response","content":[{"type":"tool_use","id":"call","name":"Bash","input":{"command":"make test"}}],"stop_reason":"tool_use"}`

	for _, tc := range []struct {
		name    string
		latest  translate.EscalationMessage
		served  string
		failure llmescalation.FailureCode
	}{
		{name: "new prompt", latest: prompt("use the staging config instead"), failure: llmescalation.FailureRecentUserPrompt},
		{name: "tool loop", latest: toolResult, failure: llmescalation.FailureNone},
		{name: "tool loop below escalation target", latest: toolResult, served: "gpt-5.6-luna", failure: llmescalation.FailureNone},
		{name: "tool loop on escalation target", latest: toolResult, served: "gpt-6.1-sol", failure: llmescalation.FailureServedTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &llmEscalationStoreStub{}
			service := newTestService().WithLLMEscalation(store, nil).WithQwenEscalation(&blockingEscalationJudge{})
			turn := &llmTurn{
				observation: translate.EscalationObservation{Messages: append(slices.Clone(history), tc.latest)},
				session:     llmescalation.Session{CompletedTurns: 11, Config: llmescalation.Config{Cadence: 3, Classifier: flags.EscalationClassifierLLM}},
			}
			capture := newCaptureWriter()
			_, err := capture.Write([]byte(toolCallResponse))
			require.NoError(t, err)
			ctx := context.WithValue(context.Background(), servedTargetsContextKey{}, []string{"gpt-6.1-sol"})
			service.completeLLMEscalation(ctx, testResult{llmEscalation: turn, Decision: router.Decision{Model: tc.served}}, capture, translate.EscalationResponseAnthropic)
			require.Equal(t, 1, store.completeCalls)
			require.Equal(t, tc.failure, store.completionRequest.IntervalFailure)
		})
	}
}

func TestLLMEscalationActiveRolloutGateAppliesToRequestOverrides(t *testing.T) {
	overrides := flags.Overrides{Strings: map[flags.Key]string{
		flags.KeyEscalationActiveClassifier: string(flags.EscalationClassifierSwitchyard),
	}}
	ctx := flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyHMMEmbedding), overrides)
	service := newTestService().
		WithLLMEscalation(&llmEscalationStoreStub{}, &blockingEscalationJudge{}).
		WithEscalationConfiguration(nil, nil, false)
	requestResult := testResult{Strategy: router.StrategyHMMEmbedding, InstallationID: uuid.New(), TurnType: turntype.MainLoop}

	rejected := service.beginLLMEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &requestResult)
	require.Nil(t, rejected)

	service.WithSwitchyardActive(true)
	accepted := service.beginLLMEscalation(ctx, escalationTestEnvelope(t, 1), router.Request{}, &requestResult)
	require.NotNil(t, accepted)
	require.True(t, accepted.active)
}

func TestLLMEscalationContinuationFailureStillCompletesTurn(t *testing.T) {
	store := &llmEscalationStoreStub{continuationErr: errors.New("continuation unavailable")}
	service := newTestService().WithLLMEscalation(store, &blockingEscalationJudge{})
	turn := &llmTurn{session: llmescalation.Session{Config: llmescalation.Config{Cadence: 3}}}
	capture := newCaptureWriter()
	_, err := capture.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`))
	require.NoError(t, err)

	service.completeLLMEscalation(context.Background(), testResult{llmEscalation: turn}, capture, translate.EscalationResponseResponses)
	require.Equal(t, 1, store.completeCalls)
}

func TestLLMEscalationFailureRecognizesCanceledDeadline(t *testing.T) {
	require.Equal(t, llmescalation.FailureTimeout, llmEscalationFailure(context.Canceled, context.DeadlineExceeded))
	require.Equal(t, llmescalation.FailureJudge, llmEscalationFailure(context.Canceled, nil))
	require.Equal(t, llmescalation.FailureInvalid, llmEscalationFailure(llmescalation.ErrInvalidEscalationJudgment, nil))
}

var _ llmescalation.Store = (*llmEscalationStoreStub)(nil)

var _ llmescalation.Judge = (*blockingEscalationJudge)(nil)
