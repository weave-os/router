package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const llmEscalationBookkeepingTimeout = 100 * time.Millisecond
const llmEscalationPinTier = "escalation_switchyard_llm_v1"

type llmEscalationTurn struct {
	active       bool
	activation   [32]byte
	boundary     [32]byte
	requestID    string
	observation  translate.EscalationObservation
	session      llmescalation.Session
	retainReason bool
	applied      bool
}

// WithLLMEscalation wires optional background judging without altering XGBoost.
func (s *Service) WithLLMEscalation(store llmescalation.Store, judge llmescalation.Judge) *Service {
	s.llmEscalationStore = store
	s.llmEscalationJudge = judge
	s.llmEscalationSlots = make(chan struct{}, llmescalation.MaxWorkers)
	return s
}

func (t *llmEscalationTurn) constraint() *escalation.Constraint {
	if !t.active {
		return nil
	}
	positive := t.session.Pending != nil && t.session.Pending.Judgment != nil && t.session.Pending.Judgment.Escalate
	if !positive && t.session.Floor == "" {
		return nil
	}
	return &escalation.Constraint{Floor: t.session.Floor, Escalate: positive}
}

func (s *Service) beginLLMEscalation(ctx context.Context, env *translate.RequestEnvelope, req router.Request, res *turnLoopResult, apiKeyID string) *llmEscalationTurn {
	selection := flags.EscalationFromContext(ctx)
	active := selection.Active == flags.EscalationClassifierSwitchyard && s.llmEscalationActiveEnabled
	shadow := selection.Shadow == flags.EscalationClassifierSwitchyard
	if (!active && !shadow) || s.llmEscalationStore == nil || s.llmEscalationJudge == nil || (active && res.Strategy != router.StrategyHMMEmbedding) || req.ShadowMode || req.ForceModel != "" || req.ForceCluster != "" || res.InstallationID == uuid.Nil || (res.TurnType != turntype.MainLoop && res.TurnType != turntype.ToolResult) {
		return nil
	}
	if len(req.GatewayProviders) > 0 || slices.Contains(installationExcludedProvidersFromContext(ctx), providers.ProviderOpenRouter) {
		observability.FromContext(ctx).Info("LLM escalation skipped", "reason", "provider_restricted")
		return nil
	}
	if _, excluded := req.ExcludedModels[policy.EscalationJudgeModel]; excluded {
		observability.FromContext(ctx).Info("LLM escalation skipped", "reason", "judge_model_restricted")
		return nil
	}
	mode := llmescalation.ModeShadow
	if active {
		mode = llmescalation.ModeActive
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%d", llmescalation.Version, llmescalation.SwitchyardRevision, llmescalation.SystemPrompt, llmescalation.ResponseSchema, selection.Cadence)))
	config := llmescalation.Config{Mode: mode, Epoch: selection.Epoch, Cadence: selection.Cadence, Digest: fmt.Sprintf("%x", digest)}
	scope := sha256.Sum256([]byte(fmt.Sprintf("%s/%x/%s/%s/%d/%s", res.InstallationID, res.SessionKey, res.Strategy, mode, selection.Epoch, config.Digest)))
	activation := escalationActivationID(res.InstallationID, fmt.Sprintf("%s/%s/%s/%d/%s", apiKeyID, res.Strategy, mode, selection.Epoch, config.Digest))
	observation, err := env.EscalationObservation()
	if original, ok := ctx.Value(nativeResponsesBodyContextKey{}).([]byte); ok {
		observation, err = translate.ParseResponsesEscalationObservation(original)
	}
	if err != nil {
		observability.FromContext(ctx).Warn("LLM escalation observation unavailable", "error_type", fmt.Sprintf("%T", err))
		return nil
	}
	bookkeepingCtx, cancel := context.WithTimeout(ctx, llmEscalationBookkeepingTimeout)
	defer cancel()
	if observation.ContinuationID != "" {
		prior, found, lookupErr := s.llmEscalationStore.Continuation(bookkeepingCtx, activation, observation.ContinuationID)
		if lookupErr != nil || !found {
			observability.FromContext(ctx).Warn("LLM escalation continuation unavailable", "err", lookupErr, "found", found)
			return nil
		}
		var messages []translate.EscalationMessage
		if err := json.Unmarshal(prior.History, &messages); err != nil {
			observability.FromContext(ctx).Warn("LLM escalation continuation invalid", "error_type", fmt.Sprintf("%T", err))
			return nil
		}
		observation.Messages = mergeLLMEscalationHistory(messages, observation.Messages)
		observation.ContinuationID = ""
		observation.HistoryComplete = len(observation.ItemReferenceIDs) == 0
		scope = prior.Scope
	}
	if !observation.HistoryComplete || len(observation.ItemReferenceIDs) > 0 {
		observability.FromContext(ctx).Info("LLM escalation skipped", "reason", "history_unavailable")
		return nil
	}
	observation.Messages = translate.WithoutEscalationDecorations(observation.Messages)
	encoded, _ := json.Marshal(observation)
	if len(encoded) > escalationHistoryMaxBytes {
		observability.FromContext(ctx).Info("LLM escalation skipped", "reason", "history_too_large")
		return nil
	}
	session, err := s.llmEscalationStore.Start(bookkeepingCtx, llmescalation.StartRequest{Scope: scope, InstallationID: res.InstallationID.String(), InstructionFingerprint: llmescalation.InstructionFingerprint(observation.Messages), Config: config})
	if err != nil {
		observability.FromContext(ctx).Warn("LLM escalation state unavailable", "err", err)
		return nil
	}
	return &llmEscalationTurn{active: active, activation: activation, boundary: sha256.Sum256(encoded), requestID: observability.RequestIDFromContext(ctx), observation: observation, session: session, retainReason: s.effectiveCaptureMode(ctx) == CaptureFull}
}

func mergeLLMEscalationHistory(prior, current []translate.EscalationMessage) []translate.EscalationMessage {
	instructions := 0
	for instructions < len(current) && llmEscalationInstructionRole(current[instructions].Role) {
		instructions++
	}
	if instructions > 0 {
		for len(prior) > 0 && llmEscalationInstructionRole(prior[0].Role) {
			prior = prior[1:]
		}
	}
	merged := make([]translate.EscalationMessage, 0, len(prior)+len(current))
	merged = append(merged, current[:instructions]...)
	merged = append(merged, prior...)
	return append(merged, current[instructions:]...)
}

func llmEscalationInstructionRole(role translate.EscalationRole) bool {
	return role == translate.EscalationRoleSystem || role == translate.EscalationRoleDeveloper
}

func (s *Service) applyLLMEscalation(ctx context.Context, turn *llmEscalationTurn, decision router.Decision) error {
	if turn.session.Pending == nil || turn.session.Pending.Judgment == nil || !turn.session.Pending.Judgment.Escalate || turn.session.Floor != "" {
		return nil
	}
	if turn.active && !escalationRoutingApplied(decision) {
		recordCtx, cancel := context.WithTimeout(ctx, llmEscalationBookkeepingTimeout)
		defer cancel()
		return s.llmEscalationStore.RecordNoTarget(recordCtx, llmescalation.ApplyRequest{Session: turn.session, JobID: turn.session.Pending.ID, RequestID: turn.requestID, Turn: turn.session.CompletedTurns + 1})
	}
	applyCtx, cancel := context.WithTimeout(ctx, llmEscalationBookkeepingTimeout)
	defer cancel()
	applied, err := s.llmEscalationStore.Apply(applyCtx, llmescalation.ApplyRequest{Session: turn.session, JobID: turn.session.Pending.ID, Floor: escalation.Maximum, RequestID: turn.requestID, Turn: turn.session.CompletedTurns + 1})
	if err != nil {
		return err
	}
	if !applied {
		return errors.New("LLM escalation verdict superseded before application")
	}
	turn.applied = true
	return nil
}

func (s *Service) completeLLMEscalation(ctx context.Context, res turnLoopResult, proxyErr error, capture *captureWriter, format translate.EscalationResponseFormat) {
	turn := res.llmEscalation
	if turn == nil || proxyErr != nil {
		return
	}
	body, truncated := capturedResponse(capture)
	if truncated || len(body) == 0 {
		observability.FromContext(ctx).Info("LLM escalation completion skipped", "reason", "capture_unavailable")
		return
	}
	completed, err := translate.ParseEscalationResponse(body, format)
	if err != nil {
		observability.FromContext(ctx).Info("LLM escalation completion skipped", "reason", "incomplete_response")
		return
	}
	messages := make([]translate.EscalationMessage, 0, len(turn.observation.Messages)+len(completed.Messages))
	messages = append(messages, turn.observation.Messages...)
	messages = append(messages, translate.WithoutEscalationDecorations(completed.Messages)...)
	if format == translate.EscalationResponseResponses && completed.ResponseID != "" {
		history, _ := json.Marshal(messages)
		if len(history) <= escalationHistoryMaxBytes {
			continuationCtx, cancelContinuation := context.WithTimeout(context.WithoutCancel(ctx), llmEscalationBookkeepingTimeout)
			err := s.llmEscalationStore.SaveContinuation(continuationCtx, llmescalation.ContinuationRequest{Session: turn.session, Activation: turn.activation, ResponseID: completed.ResponseID, History: history})
			cancelContinuation()
			if err != nil {
				observability.FromContext(ctx).Warn("LLM escalation continuation save failed", "err", err)
			}
		}
	}
	if turn.applied || turn.session.Floor != "" {
		return
	}
	capacity := false
	select {
	case s.llmEscalationSlots <- struct{}{}:
		capacity = true
	default:
	}
	completionCtx, cancelCompletion := context.WithTimeout(context.WithoutCancel(ctx), llmEscalationBookkeepingTimeout)
	completion, err := s.llmEscalationStore.Complete(completionCtx, llmescalation.CompleteRequest{Session: turn.session, Boundary: turn.boundary, RequestID: turn.requestID, Capacity: capacity})
	cancelCompletion()
	if err != nil || completion.Job == nil {
		if capacity {
			<-s.llmEscalationSlots
		}
		if err != nil {
			observability.FromContext(ctx).Warn("LLM escalation completion persistence failed", "err", err)
		}
		return
	}
	job := *completion.Job
	transcript := llmescalation.RenderTranscript(messages)
	log := observability.FromContext(ctx).With("escalation_job_id", job.ID, "escalation_checkpoint", job.Checkpoint)
	observability.SafeGo(log, llmescalation.JudgeTimeout+time.Second, "escalation-judge", func(background context.Context) {
		defer func() { <-s.llmEscalationSlots }()
		background = observability.WithLogger(background, log)
		background = observability.WithRequestID(background, turn.requestID)
		judgeCtx, judgeCancel := context.WithTimeout(background, llmescalation.JudgeTimeout)
		judgment, judgeErr := s.llmEscalationJudge.Judge(judgeCtx, llmescalation.JudgeRequest{Transcript: transcript, RequestID: turn.requestID, OperationID: job.ID})
		judgeContextErr := judgeCtx.Err()
		judgeCancel()
		failure := llmEscalationFailure(judgeErr, judgeContextErr)
		if judgeErr != nil {
			log.Warn("LLM escalation judge failed", "failure", failure, "error_type", fmt.Sprintf("%T", judgeErr))
		}
		if !turn.retainReason {
			judgment.Reason = ""
		} else {
			judgment.Reason = s.redact([]byte(judgment.Reason), ContentKindResponse)
		}
		persistCtx, persistCancel := context.WithTimeout(background, llmEscalationBookkeepingTimeout)
		defer persistCancel()
		if err := s.llmEscalationStore.FinishJob(persistCtx, job, judgment, failure); err != nil {
			log.Warn("LLM escalation verdict persistence failed", "err", err)
		}
		s.recordEscalationJudgeInference(turn.session.InstallationID, turn.requestID, job, judgment)
		log.Info("LLM escalation checkpoint finished", "failure", failure, "escalate", judgment.Escalate, "usage_known", judgment.Usage.Known, "input_tokens", judgment.Usage.InputTokens, "output_tokens", judgment.Usage.OutputTokens, "cost_usd", judgment.CostUSD)
	})
}

func llmEscalationFailure(judgeErr, judgeContextErr error) llmescalation.FailureCode {
	switch {
	case judgeErr == nil:
		return llmescalation.FailureNone
	case errors.Is(judgeErr, context.DeadlineExceeded), errors.Is(judgeErr, context.Canceled) && errors.Is(judgeContextErr, context.DeadlineExceeded):
		return llmescalation.FailureTimeout
	case errors.Is(judgeErr, ErrInvalidEscalationJudgment):
		return llmescalation.FailureInvalid
	default:
		return llmescalation.FailureJudge
	}
}
