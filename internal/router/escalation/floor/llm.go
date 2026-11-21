package floor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type llmTurn struct {
	active       bool
	activation   [32]byte
	boundary     [32]byte
	requestID    string
	observation  translate.EscalationObservation
	session      llmescalation.Session
	retainReason bool
	applied      bool
}

func (t *llmTurn) constraint() *escalation.Constraint {
	if !t.active {
		return nil
	}
	return t.verdict()
}

func (t *llmTurn) verdict() *escalation.Constraint {
	positive := t.session.Pending != nil && t.session.Pending.Judgment != nil && t.session.Pending.Judgment.Escalate
	if !positive && t.session.Floor == "" {
		return nil
	}
	return &escalation.Constraint{Floor: t.session.Floor, Escalate: positive}
}

func (h *Helper) beginLLM(ctx context.Context, facts Facts, req router.Request) *llmTurn {
	selection := h.selection(ctx)
	active := flags.IsLLMEscalationClassifier(selection.Active) && (selection.Active != flags.EscalationClassifierSwitchyard || h.switchyardActiveEnabled)
	shadow := flags.IsLLMEscalationClassifier(selection.Shadow)
	classifier := selection.Shadow
	if active {
		classifier = selection.Active
	}
	judge := h.llmJudge
	if classifier == flags.EscalationClassifierLLM {
		judge = h.qwenJudge
	}
	if (!active && !shadow) || h.llmStore == nil || judge == nil || (active && facts.Strategy != router.StrategyHMMEmbedding) || req.ShadowMode || req.ForceModel != "" || req.ForceCluster != "" || facts.InstallationID == uuid.Nil || (facts.TurnType != turntype.MainLoop && facts.TurnType != turntype.ToolResult) {
		return nil
	}
	if len(req.GatewayProviders) > 0 || (classifier == flags.EscalationClassifierSwitchyard && slices.Contains(facts.ExcludedProviders, providers.ProviderFireworks)) {
		observability.FromContext(ctx).Info("LLM escalation skipped", "reason", "provider_restricted")
		return nil
	}
	if _, excluded := req.Candidates.ExcludedModels[policy.EscalationJudgeModel]; excluded && classifier == flags.EscalationClassifierSwitchyard {
		observability.FromContext(ctx).Info("LLM escalation skipped", "reason", "judge_model_restricted")
		return nil
	}
	mode := llmescalation.ModeShadow
	if active {
		mode = llmescalation.ModeActive
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%s/%s/%d", llmescalation.Version, policy.EscalationJudgeModel, providers.ProviderFireworks, llmescalation.SwitchyardRevision, llmescalation.SystemPrompt, llmescalation.ResponseSchema, selection.Cadence)))
	config := llmescalation.Config{Mode: mode, Epoch: selection.Epoch, Cadence: selection.Cadence, Digest: fmt.Sprintf("%x", digest)}
	if classifier == flags.EscalationClassifierLLM {
		digest = sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%d", classifier, llmescalation.QwenSchemaVersion, llmescalation.QwenRendererVersion, selection.Cadence)))
		config.Classifier = classifier
		config.Digest = fmt.Sprintf("%x", digest)
	}
	scope := sha256.Sum256([]byte(fmt.Sprintf("%s/%x/%s/%s/%d/%s", facts.InstallationID, facts.SessionKey, facts.Strategy, mode, selection.Epoch, config.Digest)))
	activation := ActivationID(ctx, facts.InstallationID, fmt.Sprintf("%s/%s/%s/%d/%s", facts.CredentialIdentity, facts.Strategy, mode, selection.Epoch, config.Digest))
	observation, err := facts.Envelope.EscalationObservation()
	original, isNativeResponses := facts.NativeResponsesBody, facts.NativeResponses
	model := facts.Envelope.Model()
	if isNativeResponses {
		model = gjson.GetBytes(original, "model").String()
	}
	if turntype.IsSideRequest(model, facts.Envelope.LastUserMessage().Text) {
		observability.FromContext(ctx).Info("LLM escalation skipped", "reason", "side_request")
		return nil
	}
	if isNativeResponses {
		observation, err = translate.ParseResponsesEscalationObservation(original)
	}
	if err != nil {
		observability.FromContext(ctx).Warn("LLM escalation observation unavailable", "error_type", fmt.Sprintf("%T", err))
		return nil
	}
	bookkeepingCtx, cancel := context.WithTimeout(ctx, llmBookkeepingTimeout)
	defer cancel()
	if observation.ContinuationID != "" {
		prior, found, lookupErr := h.llmStore.Continuation(bookkeepingCtx, activation, observation.ContinuationID)
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
	observation.CodexToolResults = isNativeResponses && facts.CodexClient
	encoded, _ := json.Marshal(observation)
	if len(encoded) > HistoryMaxBytes {
		observability.FromContext(ctx).Info("LLM escalation skipped", "reason", "history_too_large")
		return nil
	}
	session, err := h.llmStore.Start(bookkeepingCtx, llmescalation.StartRequest{Scope: scope, InstallationID: facts.InstallationID.String(), InstructionFingerprint: llmescalation.InstructionFingerprint(observation.Messages), Config: config})
	if err != nil {
		observability.FromContext(ctx).Warn("LLM escalation state unavailable", "err", err)
		return nil
	}
	return &llmTurn{active: active, activation: activation, boundary: sha256.Sum256(encoded), requestID: observability.RequestIDFromContext(ctx), observation: observation, session: session, retainReason: facts.RetainReason}
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

func (h *Helper) ApplyLLM(ctx context.Context, t *Turn, decision router.Decision) error {
	if !t.HasLLM() {
		return nil
	}
	turn := t.llm
	if turn.session.Pending == nil || turn.session.Pending.Judgment == nil || !turn.session.Pending.Judgment.Escalate || turn.session.Floor != "" {
		return nil
	}
	if turn.active && !RoutingApplied(decision) {
		recordCtx, cancel := context.WithTimeout(ctx, llmBookkeepingTimeout)
		defer cancel()
		return h.llmStore.RecordNoTarget(recordCtx, llmescalation.ApplyRequest{Session: turn.session, JobID: turn.session.Pending.ID, RequestID: turn.requestID, Turn: turn.session.CompletedTurns + 1})
	}
	applyCtx, cancel := context.WithTimeout(ctx, llmBookkeepingTimeout)
	defer cancel()
	applied, err := h.llmStore.Apply(applyCtx, llmescalation.ApplyRequest{Session: turn.session, JobID: turn.session.Pending.ID, Floor: escalation.Maximum, RequestID: turn.requestID, Turn: turn.session.CompletedTurns + 1})
	if err != nil {
		return err
	}
	if !applied {
		return errors.New("LLM escalation verdict superseded before application")
	}
	turn.applied = true
	return nil
}

func (h *Helper) completeLLM(ctx context.Context, done Completion) {
	if !done.Turn.HasLLM() || done.ProxyErr != nil {
		return
	}
	turn, format := done.Turn.llm, done.Format
	body, truncated := done.Body, done.Truncated
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
		if len(history) <= HistoryMaxBytes {
			continuationCtx, cancelContinuation := context.WithTimeout(context.WithoutCancel(ctx), llmBookkeepingTimeout)
			err := h.llmStore.SaveContinuation(continuationCtx, llmescalation.ContinuationRequest{Session: turn.session, Activation: turn.activation, ResponseID: completed.ResponseID, History: history})
			cancelContinuation()
			if err != nil {
				observability.FromContext(ctx).Warn("LLM escalation continuation save failed", "err", err)
			}
		}
	}
	if turn.applied || turn.session.Floor != "" {
		return
	}
	transcript := llmescalation.RenderTranscript(messages)
	judge := h.llmJudge
	ready := true
	if turn.session.Config.EffectiveClassifier() == flags.EscalationClassifierLLM {
		transcript, ready = llmescalation.RenderQwenInterval(messages, turn.observation.CodexToolResults)
		judge = h.qwenJudge
		if !ready {
			observability.FromContext(ctx).Info("LLM escalation interval unavailable", "completed_turns", turn.session.CompletedTurns+1)
		}
	}
	intervalFailure := llmescalation.FailureNone
	if !ready {
		intervalFailure = llmescalation.FailureIntervalUnavailable
	} else if llmescalation.TurnsSinceUserPrompt(messages) < llmescalation.MinTurnsAfterUserPrompt {
		ready = false
		intervalFailure = llmescalation.FailureRecentUserPrompt
	} else if done.ServedTarget {
		ready = false
		intervalFailure = llmescalation.FailureServedTarget
	}
	slots := h.shadowSlots
	if turn.active {
		slots = h.slots
	}
	capacity := false
	if ready {
		select {
		case slots <- struct{}{}:
			capacity = true
		default:
		}
	}
	completionCtx, cancelCompletion := context.WithTimeout(context.WithoutCancel(ctx), llmBookkeepingTimeout)
	completion, err := h.llmStore.Complete(completionCtx, llmescalation.CompleteRequest{Session: turn.session, Boundary: turn.boundary, RequestID: turn.requestID, Capacity: capacity, IntervalFailure: intervalFailure})
	cancelCompletion()
	if err != nil || completion.Job == nil {
		if capacity {
			<-slots
		}
		if err != nil {
			observability.FromContext(ctx).Warn("LLM escalation completion persistence failed", "err", err)
		}
		return
	}
	job := *completion.Job
	log := observability.FromContext(ctx).With("escalation_job_id", job.ID, "escalation_checkpoint", job.Checkpoint, "escalation_mode", turn.session.Config.Mode, "escalation_classifier", job.Version, "installation_id", turn.session.InstallationID)
	observability.SafeGoContext(requestcontext.WithoutValues(ctx), log, llmescalation.JudgeTimeout+time.Second, "escalation-judge", func(background context.Context) {
		defer func() { <-slots }()
		background = observability.WithLogger(background, log)
		background = observability.WithRequestID(background, turn.requestID)
		judgeCtx, judgeCancel := context.WithTimeout(background, llmescalation.JudgeTimeout)
		judgment, judgeErr := judge.Judge(judgeCtx, llmescalation.JudgeRequest{Transcript: transcript, RequestID: turn.requestID, OperationID: job.ID})
		judgeContextErr := judgeCtx.Err()
		judgeCancel()
		failure := llmEscalationFailure(judgeErr, judgeContextErr)
		if judgeErr != nil {
			log.Warn("LLM escalation judge failed", "failure", failure, "error_type", fmt.Sprintf("%T", judgeErr))
		}
		if !turn.retainReason {
			judgment.Reason = ""
		} else {
			judgment.Reason = h.redactReason(judgment.Reason)
		}
		persistCtx, persistCancel := context.WithTimeout(background, llmBookkeepingTimeout)
		defer persistCancel()
		job.RecordServedRelease(judgment.Release)
		if err := h.llmStore.FinishJob(persistCtx, job, judgment, failure); err != nil {
			log.Warn("LLM escalation verdict persistence failed", "err", err)
		}
		h.recordJudge(background, turn.session.InstallationID, turn.requestID, turn.session.Config.Mode, job, judgment, failure)
		log.Info("LLM escalation checkpoint finished", "failure", failure, "escalate", judgment.Escalate, "usage_known", judgment.Usage.Known, "input_tokens", judgment.Usage.InputTokens, "output_tokens", judgment.Usage.OutputTokens, "cost_usd", judgment.CostUSD)
	})
}

func llmEscalationFailure(judgeErr, judgeContextErr error) llmescalation.FailureCode {
	switch {
	case judgeErr == nil:
		return llmescalation.FailureNone
	case errors.Is(judgeErr, context.DeadlineExceeded), errors.Is(judgeErr, context.Canceled) && errors.Is(judgeContextErr, context.DeadlineExceeded):
		return llmescalation.FailureTimeout
	case errors.Is(judgeErr, llmescalation.ErrInvalidEscalationJudgment), errors.Is(judgeErr, llmescalation.ErrInvalidJudgment):
		return llmescalation.FailureInvalid
	default:
		return llmescalation.FailureJudge
	}
}
