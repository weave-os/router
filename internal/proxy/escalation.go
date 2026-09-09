package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/google/uuid"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const escalationTimeout = 500 * time.Millisecond
const escalationHistoryMaxBytes = 1024 * 1024

type escalationMode string

const (
	escalationModeActive escalationMode = "active"
	escalationModeShadow escalationMode = "shadow"
)

// WithEscalation wires optional scoring independently of baseline HMM availability.
func (s *Service) WithEscalation(store escalation.Store, observer escalation.Observer) *Service {
	s.escalationStore = store
	s.escalationObserver = observer
	return s
}

type escalationTurn struct {
	activation  [32]byte
	observation translate.EscalationObservation
	scope       [32]byte
	boundary    [32]byte
	token       string
	session     escalation.Session
	checkpoint  escalation.Checkpoint
	active      bool
	replay      bool
}

func (t *escalationTurn) constraint() *escalation.Constraint {
	if !t.active {
		return nil
	}
	positive := !t.replay && t.checkpoint.Prediction != nil && t.checkpoint.Prediction.Escalate
	if !positive && t.session.Floor == "" {
		return nil
	}
	return &escalation.Constraint{Floor: t.session.Floor, Escalate: positive}
}

func (s *Service) beginEscalation(ctx context.Context, env *translate.RequestEnvelope, req router.Request, res *turnLoopResult, apiKeyID string) *escalationTurn {
	active := flags.BoolOr(ctx, flags.KeyEscalationXGBoostEnabled, false)
	shadow := flags.BoolOr(ctx, flags.KeyEscalationXGBoostShadowEnabled, false)
	if (!active && !shadow) || s.escalationStore == nil || s.escalationObserver == nil || res.Strategy != router.StrategyHMMEmbedding || req.ShadowMode || req.ForceModel != "" || req.ForceCluster != "" || res.InstallationID == uuid.Nil || (res.TurnType != turntype.MainLoop && res.TurnType != turntype.ToolResult) {
		return nil
	}
	mode := escalationModeShadow
	if active {
		mode = escalationModeActive
	}
	scope := sha256.Sum256([]byte(fmt.Sprintf("%s/%x/%s/%s/%d", res.InstallationID, res.SessionKey, res.Strategy, mode, flags.IntOr(ctx, flags.KeyEscalationXGBoostEpoch, 0))))
	activation := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%d", res.InstallationID, apiKeyID, res.Strategy, mode, flags.IntOr(ctx, flags.KeyEscalationXGBoostEpoch, 0))))
	log := observability.FromContext(ctx).With("escalation_scope", fmt.Sprintf("%x", scope))
	observation, err := env.EscalationObservation()
	if original, ok := ctx.Value(nativeResponsesBodyContextKey{}).([]byte); ok {
		observation, err = translate.ParseResponsesEscalationObservation(original)
	}
	if err != nil {
		s.invalidateEscalation(ctx, scope, [32]byte{}, "")
		log.Warn("Escalation input unavailable", "error_type", fmt.Sprintf("%T", err))
		return nil
	}
	encodedObservation, _ := json.Marshal(observation)
	turn := &escalationTurn{activation: activation, scope: scope, boundary: sha256.Sum256(encodedObservation), token: uuid.NewString(), active: active}
	claimCtx, cancel := context.WithTimeout(ctx, escalationTimeout)
	defer cancel()
	if observation.ContinuationID != "" {
		priorScope, history, found, lookupErr := s.escalationStore.Continuation(claimCtx, activation, observation.ContinuationID)
		if lookupErr == nil && !found {
			retryBackoff := backoff.NewExponentialBackOff()
			retryBackoff.InitialInterval = 10 * time.Millisecond
			retryBackoff.MaxInterval = 25 * time.Millisecond
			_, lookupErr = backoff.Retry(claimCtx, func() (bool, error) {
				priorScope, history, found, lookupErr = s.escalationStore.Continuation(claimCtx, activation, observation.ContinuationID)
				if lookupErr != nil {
					return false, backoff.Permanent(lookupErr)
				}
				if !found {
					return false, errors.New("escalation continuation still pending")
				}
				return true, nil
			}, backoff.WithBackOff(retryBackoff), backoff.WithMaxTries(5), backoff.WithMaxElapsedTime(100*time.Millisecond))
		}
		if lookupErr != nil || !found {
			log.Warn("Escalation continuation unavailable", "err", lookupErr, "found", found)
			return nil
		}
		var priorMessages []translate.EscalationMessage
		if decodeErr := json.Unmarshal(history, &priorMessages); decodeErr != nil {
			log.Warn("Escalation stored history invalid", "err", decodeErr)
			return nil
		}
		currentMessages := observation.Messages
		currentInstructions := make([]translate.EscalationMessage, 0)
		for len(currentMessages) > 0 && currentMessages[0].Role == translate.EscalationRoleSystem {
			currentInstructions = append(currentInstructions, currentMessages[0])
			currentMessages = currentMessages[1:]
		}
		if len(currentInstructions) > 0 {
			for len(priorMessages) > 0 && priorMessages[0].Role == translate.EscalationRoleSystem {
				priorMessages = priorMessages[1:]
			}
			priorMessages = append(currentInstructions, priorMessages...)
		}
		observation.Messages = append(priorMessages, currentMessages...)
		observation.HistoryComplete = len(observation.ItemReferenceIDs) == 0
		observation.ContinuationID = ""
		scope, turn.scope = priorScope, priorScope
		encodedObservation, _ = json.Marshal(observation)
	}
	turn.observation = observation
	session, claimed, err := s.escalationStore.Claim(claimCtx, scope, res.InstallationID.String(), turn.token, turn.boundary)
	if err != nil || !claimed {
		s.invalidateEscalation(ctx, scope, turn.boundary, "")
		log.Warn("Escalation observation not claimed", "err", err, "claimed", claimed)
		return nil
	}
	turn.session = session
	checkpoint, found, err := s.escalationStore.Checkpoint(claimCtx, scope, turn.boundary)
	if err != nil {
		s.releaseEscalation(ctx, turn)
		log.Warn("Escalation checkpoint lookup failed", "err", err)
		return nil
	}
	if found {
		turn.checkpoint = checkpoint
		turn.replay = true
		return turn
	}
	nextOrdinal := session.Ordinal + 1
	observed, err := s.escalationObserver.ObserveEscalation(claimCtx, escalation.ObserveRequest{Observation: encodedObservation, State: session.FeatureState, PreviousOutcome: session.PreviousOutcome, PredictDue: (session.FeatureTurns+1)%5 == 0})
	if err == nil {
		var state struct {
			ObservedTurns int64 `json:"observed_turns"`
		}
		if json.Unmarshal(observed.State, &state) != nil || state.ObservedTurns != session.FeatureTurns+1 {
			err = errors.New("escalation feature ordinal mismatch")
		}
	}
	if err != nil {
		// A missed observation breaks temporal continuity. Clear the reducer and
		// restart cadence while retaining the already accepted classification floor.
		turn.session.Ordinal = nextOrdinal
		turn.session.FeatureTurns = 0
		turn.session.FeatureState = nil
		turn.session.PreviousOutcome = nil
		turn.checkpoint = escalation.Checkpoint{Ordinal: nextOrdinal}
		log.Warn("Escalation observation failed; restarting feature history", "err", err, "ordinal", nextOrdinal)
		return turn
	}
	turn.session.Ordinal = nextOrdinal
	turn.session.FeatureTurns++
	turn.session.FeatureState = observed.State
	turn.session.PreviousOutcome = nil
	turn.session.ModelID = observed.ModelID
	turn.session.PackageSHA256 = observed.PackageSHA256
	turn.checkpoint = escalation.Checkpoint{Ordinal: nextOrdinal, Prediction: observed.Prediction}
	return turn
}

func (s *Service) invalidateEscalation(ctx context.Context, scope, boundary [32]byte, failedToken string) {
	invalidateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := s.escalationStore.Invalidate(invalidateCtx, scope, boundary, failedToken); err != nil {
		observability.FromContext(ctx).Warn("Escalation continuity reset failed", "err", err)
	}
}

func (s *Service) releaseEscalation(ctx context.Context, turn *escalationTurn) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := s.escalationStore.Release(releaseCtx, turn.scope, turn.token); err != nil {
		observability.FromContext(ctx).Warn("Escalation claim release failed", "err", err, "ordinal", turn.session.Ordinal)
	}
}

func escalationRoutingApplied(decision router.Decision) bool {
	if decision.Metadata == nil || decision.Metadata.Escalation == nil {
		return false
	}
	intervention := decision.Metadata.Escalation
	return intervention.Constrained
}

func (s *Service) finishEscalation(ctx context.Context, turn *escalationTurn, res *turnLoopResult, routeErr error) error {
	if turn.replay {
		res.EscalationShadowMarked = !turn.active && turn.checkpoint.Prediction != nil && turn.checkpoint.Prediction.Escalate
		res.EscalationScope = turn.scope
		res.EscalationOrdinal = turn.checkpoint.Ordinal
		res.escalationActivation = turn.activation
		res.escalationObservation = turn.observation
		s.releaseEscalation(ctx, turn)
		return nil
	}
	if routeErr == nil && turn.active && res.Decision.Metadata != nil {
		turn.checkpoint.Decision = res.Decision.Metadata.Escalation
		if intervention := turn.checkpoint.Decision; intervention != nil && intervention.Outcome == escalation.OutcomePromoted && escalationRoutingApplied(res.Decision) {
			turn.session.Floor = escalation.Higher(turn.session.Floor, intervention.Effective)
		}
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := s.escalationStore.Commit(commitCtx, turn.scope, turn.boundary, turn.token, turn.session, turn.checkpoint); err != nil {
		// Reset the gap and release the lease atomically. A committed boundary
		// remains intact if only its acknowledgment was lost.
		s.invalidateEscalation(ctx, turn.scope, turn.boundary, turn.token)
		return err
	}
	res.EscalationShadowMarked = !turn.active && turn.checkpoint.Prediction != nil && turn.checkpoint.Prediction.Escalate
	res.EscalationScope = turn.scope
	res.EscalationOrdinal = turn.session.Ordinal
	res.escalationActivation = turn.activation
	res.escalationObservation = turn.observation
	if prediction := turn.checkpoint.Prediction; prediction != nil {
		observability.FromContext(ctx).Info("Escalation checkpoint evaluated", "ordinal", turn.checkpoint.Ordinal, "active", turn.active, "model_id", turn.session.ModelID, "package_sha256", turn.session.PackageSHA256, "score", prediction.Score, "threshold", prediction.Threshold, "escalate", prediction.Escalate, "floor", turn.session.Floor, "served_group", decisionPolicyGroup(res.Decision), "served_model", res.Decision.Model)
	}
	return nil
}

// captureEscalationResponse observes client-visible output solely for the opted-in
// session's bounded operational state, independent of training/content-log consent.
func (s *Service) captureEscalationResponse(w http.ResponseWriter, res turnLoopResult) (http.ResponseWriter, *captureWriter) {
	if res.EscalationOrdinal == 0 {
		return w, nil
	}
	if responses, ok := w.(*translate.ResponsesWriter); ok {
		var capture *captureWriter
		responses.WrapInner(func(inner http.ResponseWriter) http.ResponseWriter {
			capture = newCaptureWriter(inner, escalationHistoryMaxBytes)
			return capture
		})
		return w, capture
	}
	capture := newCaptureWriter(w, escalationHistoryMaxBytes)
	return capture, capture
}

func (s *Service) completeEscalation(ctx context.Context, res turnLoopResult, proxyErr error, capture *captureWriter, format translate.EscalationResponseFormat) {
	if res.EscalationOrdinal == 0 {
		return
	}
	if deferred := deferredCallLogFrom(ctx); deferred != nil {
		deferred.escalation = func(finalErr error) {
			s.recordEscalationOutcome(ctx, res, finalErr, capture, translate.EscalationResponseResponses)
		}
		return
	}
	s.recordEscalationOutcome(ctx, res, proxyErr, capture, format)
}

func (s *Service) recordEscalationOutcome(ctx context.Context, res turnLoopResult, proxyErr error, capture *captureWriter, format translate.EscalationResponseFormat) {
	if s.escalationStore == nil || res.EscalationOrdinal == 0 {
		return
	}
	outcome := escalation.PreviousOutcome{IsError: proxyErr != nil, StatusCode: 200}
	var upstream *providers.UpstreamErrorResponse
	if errors.As(proxyErr, &upstream) {
		outcome.StatusCode = upstream.Status
	} else if proxyErr != nil {
		outcome.StatusCode = 500
	}
	outcomeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	body, truncated := capturedResponse(capture)
	var completed translate.EscalationResponse
	if !truncated && len(body) > 0 {
		if parsed, parseErr := translate.ParseEscalationResponse(body, format); parseErr == nil {
			completed = parsed
			toolCalls := make([]translate.EscalationBlock, 0)
			for _, message := range completed.Messages {
				for _, block := range message.Blocks {
					if block.Type == translate.EscalationBlockToolCall {
						toolCalls = append(toolCalls, block)
					}
				}
			}
			outcome.ToolCalls, _ = json.Marshal(toolCalls)
		} else {
			observability.FromContext(ctx).Warn("Escalation response evidence unavailable", "error_type", fmt.Sprintf("%T", parseErr), "ordinal", res.EscalationOrdinal)
		}
	}
	if format == translate.EscalationResponseResponses && completed.ResponseID != "" && proxyErr == nil && res.escalationObservation.HistoryComplete {
		history, _ := json.Marshal(append(res.escalationObservation.Messages, completed.Messages...))
		if len(history) <= escalationHistoryMaxBytes {
			if err := s.escalationStore.SaveContinuation(outcomeCtx, res.escalationActivation, completed.ResponseID, res.EscalationScope, res.EscalationOrdinal, history); err != nil {
				observability.FromContext(ctx).Warn("Escalation continuation persistence failed", "err", err, "ordinal", res.EscalationOrdinal)
			}
		}
	}
	if err := s.escalationStore.SaveOutcome(outcomeCtx, res.EscalationScope, res.EscalationOrdinal, outcome); err != nil {
		observability.FromContext(ctx).Warn("Escalation prior outcome persistence failed", "err", err, "ordinal", res.EscalationOrdinal)
	}
}
