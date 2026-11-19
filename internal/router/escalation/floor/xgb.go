package floor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/google/uuid"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type xgbTurn struct {
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

func (t *xgbTurn) constraint() *escalation.Constraint {
	if !t.active {
		return nil
	}
	positive := !t.replay && t.checkpoint.Prediction != nil && t.checkpoint.Prediction.Escalate
	if !positive && t.session.Floor == "" {
		return nil
	}
	return &escalation.Constraint{Floor: t.session.Floor, Escalate: positive}
}

func (h *Helper) beginXGB(ctx context.Context, facts Facts, req router.Request) *xgbTurn {
	selection := h.selection(ctx)
	active := selection.Active == flags.EscalationClassifierXGB
	shadow := selection.Shadow == flags.EscalationClassifierXGB
	if (!active && !shadow) || h.store == nil || h.observer == nil || (active && facts.Strategy != router.StrategyHMMEmbedding) || req.ShadowMode || req.ForceModel != "" || req.ForceCluster != "" || facts.InstallationID == uuid.Nil || (facts.TurnType != turntype.MainLoop && facts.TurnType != turntype.ToolResult) {
		return nil
	}
	mode := escalation.ModeShadow
	if active {
		mode = escalation.ModeActive
	}
	scope := sha256.Sum256([]byte(fmt.Sprintf("%s/%x/%s/%s/%d", facts.InstallationID, facts.SessionKey, facts.Strategy, mode, selection.Epoch)))
	activation := ActivationID(ctx, facts.InstallationID, fmt.Sprintf("%s/%s/%s/%d", facts.CredentialIdentity, facts.Strategy, mode, selection.Epoch))
	log := observability.FromContext(ctx).With("escalation_scope", fmt.Sprintf("%x", scope))
	observation, err := facts.Envelope.EscalationObservation()
	if facts.NativeResponses {
		observation, err = translate.ParseResponsesEscalationObservation(facts.NativeResponsesBody)
	}
	if err != nil {
		h.invalidateXGB(ctx, scope, [32]byte{}, "")
		log.Warn("Escalation input unavailable", "error_type", fmt.Sprintf("%T", err))
		return nil
	}
	observation.Messages = translate.WithDeveloperRolesAsSystem(observation.Messages)
	encodedObservation, _ := json.Marshal(observation)
	turn := &xgbTurn{activation: activation, scope: scope, boundary: sha256.Sum256(encodedObservation), token: uuid.NewString(), active: active}
	claimCtx, cancel := context.WithTimeout(ctx, xgbTimeout)
	defer cancel()
	if observation.ContinuationID != "" {
		priorScope, history, found, lookupErr := h.store.Continuation(claimCtx, activation, observation.ContinuationID)
		if lookupErr == nil && !found {
			retryBackoff := backoff.NewExponentialBackOff()
			retryBackoff.InitialInterval = 10 * time.Millisecond
			retryBackoff.MaxInterval = 25 * time.Millisecond
			_, _ = backoff.Retry(claimCtx, func() (bool, error) {
				priorScope, history, found, lookupErr = h.store.Continuation(claimCtx, activation, observation.ContinuationID)
				if lookupErr != nil {
					return false, backoff.Permanent(lookupErr)
				}
				if !found {
					return false, errors.New("escalation continuation still pending")
				}
				return true, nil
			}, backoff.WithBackOff(retryBackoff), backoff.WithMaxTries(5), backoff.WithMaxElapsedTime(100*time.Millisecond))
		}
		if _, managed := requestcontext.ServingIdentityFromContext(ctx); lookupErr == nil && !found && !managed {
			legacyActivation := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s/%d", facts.InstallationID, facts.APIKeyID, facts.Strategy, mode, selection.Epoch))) // codeql[go/weak-sensitive-data-hashing]
			priorScope, history, found, lookupErr = h.store.Continuation(claimCtx, legacyActivation, observation.ContinuationID)
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
	session, claimed, err := h.store.Claim(claimCtx, scope, facts.InstallationID.String(), turn.token, turn.boundary)
	if err != nil || !claimed {
		h.invalidateXGB(ctx, scope, turn.boundary, "")
		log.Warn("Escalation observation not claimed", "err", err, "claimed", claimed)
		return nil
	}
	epoch := selection.Epoch
	session.Epoch = &epoch
	if active {
		session.Mode = escalation.ModeActive
	} else {
		session.Mode = escalation.ModeShadow
	}
	turn.session = session
	checkpoint, found, err := h.store.Checkpoint(claimCtx, scope, turn.boundary)
	if err != nil {
		h.releaseXGB(ctx, turn)
		log.Warn("Escalation checkpoint lookup failed", "err", err)
		return nil
	}
	if found {
		turn.checkpoint = checkpoint
		turn.replay = true
		return turn
	}
	nextOrdinal := session.Ordinal + 1
	observed, err := h.observer.ObserveEscalation(claimCtx, escalation.ObserveRequest{Observation: encodedObservation, State: session.FeatureState, PreviousOutcome: session.PreviousOutcome, PredictDue: (session.FeatureTurns+1)%5 == 0})
	if err == nil {
		var state struct {
			ObservedTurns int64 `json:"observed_turns"`
		}
		if json.Unmarshal(observed.State, &state) != nil || state.ObservedTurns != session.FeatureTurns+1 {
			err = errors.New("escalation feature ordinal mismatch")
		}
	}
	if err != nil {
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

func ActivationID(ctx context.Context, installationID uuid.UUID, material string) [32]byte {
	mac := hmac.New(sha256.New, installationID[:])
	_, _ = mac.Write([]byte(material))
	var activation [32]byte
	copy(activation[:], requestcontext.ServingStateKey(ctx, mac.Sum(nil)))
	return activation
}

func (h *Helper) invalidateXGB(ctx context.Context, scope, boundary [32]byte, failedToken string) {
	invalidateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := h.store.Invalidate(invalidateCtx, scope, boundary, failedToken); err != nil {
		observability.FromContext(ctx).Warn("Escalation continuity reset failed", "err", err)
	}
}

func (h *Helper) releaseXGB(ctx context.Context, turn *xgbTurn) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := h.store.Release(releaseCtx, turn.scope, turn.token); err != nil {
		observability.FromContext(ctx).Warn("Escalation claim release failed", "err", err, "ordinal", turn.session.Ordinal)
	}
}

func (h *Helper) Finish(ctx context.Context, t *Turn, decision router.Decision, servedGroup string, routeErr error) (Committed, error) {
	if !t.HasXGB() {
		return Committed{}, nil
	}
	turn := t.xgb
	if turn.replay {
		committed := Committed{ShadowMarked: flags.BoolOr(ctx, flags.KeyEscalationXGBoostShadowMarkerEnabled, false) && !turn.active && turn.checkpoint.Prediction != nil && turn.checkpoint.Prediction.Escalate, Scope: turn.scope, Ordinal: turn.checkpoint.Ordinal}
		h.releaseXGB(ctx, turn)
		return committed, nil
	}
	if routeErr == nil && turn.active && decision.Metadata != nil {
		turn.checkpoint.Decision = decision.Metadata.Escalation
		if intervention := turn.checkpoint.Decision; intervention != nil && intervention.Outcome == escalation.OutcomePromoted && RoutingApplied(decision) {
			turn.session.Floor = escalation.Higher(turn.session.Floor, intervention.Effective)
		}
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := h.store.Commit(commitCtx, turn.scope, turn.boundary, turn.token, turn.session, turn.checkpoint); err != nil {
		h.invalidateXGB(ctx, turn.scope, turn.boundary, turn.token)
		return Committed{}, err
	}
	committed := Committed{ShadowMarked: flags.BoolOr(ctx, flags.KeyEscalationXGBoostShadowMarkerEnabled, false) && !turn.active && turn.checkpoint.Prediction != nil && turn.checkpoint.Prediction.Escalate, Scope: turn.scope, Ordinal: turn.session.Ordinal}
	if prediction := turn.checkpoint.Prediction; prediction != nil {
		observability.FromContext(ctx).Info("Escalation checkpoint evaluated", "ordinal", turn.checkpoint.Ordinal, "active", turn.active, "model_id", turn.session.ModelID, "package_sha256", turn.session.PackageSHA256, "score", prediction.Score, "threshold", prediction.Threshold, "escalate", prediction.Escalate, "floor", turn.session.Floor, "served_group", servedGroup, "served_model", decision.Model)
	}
	return committed, nil
}

func (h *Helper) recordXGBOutcome(ctx context.Context, completion Completion) {
	if h.store == nil || completion.Committed.Ordinal == 0 {
		return
	}
	proxyErr, format := completion.ProxyErr, completion.Format
	outcome := escalation.PreviousOutcome{IsError: proxyErr != nil, StatusCode: 200}
	var upstream *providers.UpstreamErrorResponse
	if errors.As(proxyErr, &upstream) {
		outcome.StatusCode = upstream.Status
	} else if proxyErr != nil {
		outcome.StatusCode = 500
	}
	outcomeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	body, truncated := completion.Body, completion.Truncated
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
			observability.FromContext(ctx).Warn("Escalation response evidence unavailable", "error_type", fmt.Sprintf("%T", parseErr), "ordinal", completion.Committed.Ordinal)
		}
	}
	if format == translate.EscalationResponseResponses && completed.ResponseID != "" && proxyErr == nil && completion.Turn.xgb.observation.HistoryComplete {
		history, _ := json.Marshal(append(completion.Turn.xgb.observation.Messages, completed.Messages...))
		if len(history) <= HistoryMaxBytes {
			if err := h.store.SaveContinuation(outcomeCtx, completion.Turn.xgb.activation, completed.ResponseID, completion.Committed.Scope, completion.Committed.Ordinal, history); err != nil {
				observability.FromContext(ctx).Warn("Escalation continuation persistence failed", "err", err, "ordinal", completion.Committed.Ordinal)
			}
		}
	}
	if err := h.store.SaveOutcome(outcomeCtx, completion.Committed.Scope, completion.Committed.Ordinal, outcome); err != nil {
		observability.FromContext(ctx).Warn("Escalation prior outcome persistence failed", "err", err, "ordinal", completion.Committed.Ordinal)
	}
}
