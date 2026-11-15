package proxy

import (
	"context"
	"encoding/binary"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

// escalateModel is the strong model a looping cheap/mid session is rescued onto.
const escalateModel = "claude-opus-5"

// Loop-escalation action taxonomy, recorded per event. Exactly one applies.
const (
	// loopActionEscalated: the session was pinned to escalateModel.
	loopActionEscalated = "escalated"
	// loopActionHoldout: log-not-act bucket — loop detected but not escalated,
	// so the ~43% self-recovery rate can be subtracted from rescue claims.
	loopActionHoldout = "holdout"
	// loopActionAlreadyStrong: the looping model IS the escalation target — a
	// genuinely hard task, not a misroute. Record-only training signal.
	loopActionAlreadyStrong = "already_strong"
	// loopActionUserForced: a /force-model (or x-weave-force-model) pin
	// outranks auto-escalation; the forced pin is left in place.
	loopActionUserForced = "user_forced"
	// loopActionDisabled: the ROUTER_LOOP_ESCALATION_ENABLED kill switch is
	// off. Detection and telemetry continue; the pin write does not.
	loopActionDisabled = "disabled"
	// loopActionAutomaticRoutingDisabled records that the configured rescue
	// target was withdrawn from automatic routing and was not pinned.
	loopActionAutomaticRoutingDisabled = "automatic_routing_disabled"
)

// DeterministicHoldout deterministically buckets a session using the session
// key (already uniform sha256), so the bucket is stable across replicas/retries.
func DeterministicHoldout(sessionKey [sessionpin.SessionKeyLen]byte, pct int) bool {
	if pct <= 0 {
		return false
	}
	if pct >= 100 {
		return true
	}
	return int(binary.BigEndian.Uint32(sessionKey[0:4])%100) < pct
}

// handleLoopEscalation pins a session stuck in a wide tool-call cycle to opus
// and records a telemetry event; it writes no response, so normal routing
// picks up the pin and dispatches this turn. Idempotent via the pin check plus
// a durable once-per-session budget. The pin write is further gated by the
// kill switch, the log-not-act holdout, a user-forced pin, or the looping
// model already being the target — those cases still record the event
// (action column says which) but withhold the rescue.
func (s *Service) handleLoopEscalation(
	ctx context.Context,
	top translate.ToolCallSig,
	topCount int,
	distinctRatio float64,
	window int,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	routedModel string,
	isSubagent bool,
	forceModelSessionKeys ...[sessionpin.SessionKeyLen]byte,
) {
	log := observability.FromContext(ctx)

	loopingModel := routedModel
	userForced := false
	var forcePin sessionpin.Pin
	var forceModelActive, forceModelCleared bool
	if len(forceModelSessionKeys) > 0 {
		forcePin, forceModelActive, forceModelCleared = s.loadForceModelSessionPin(ctx, forceModelSessionKeys[0], sessionKey, isSubagent)
	}
	if s.pinStore != nil && installationID != uuid.Nil {
		existing, found, err := s.pinStore.Get(ctx, sessionKey, role)
		if err != nil {
			log.Error("loop-escalation: prior pin lookup failed", "err", err)
		} else if found && pinMatchesEffectiveStrategy(ctx, existing) && !(forceModelCleared && isUserForcedReason(existing.Reason)) {
			if existing.Reason == translate.ReasonLoopEscalation {
				return // already rescued this session; don't re-pin or double-log
			}
			// A user's explicit /force-model choice outranks auto-escalation —
			// record the loop for telemetry but leave the forced pin in place.
			if existing.Reason == translate.ReasonUserForceModel {
				userForced = true
			}
			if existing.Model != "" {
				loopingModel = existing.Model
			}
		}
	}

	if forceModelActive {
		userForced = true
		loopingModel = forcePin.Model
	}

	// Once-per-session budget, durable past pin TTL expiry: covers sessions
	// outliving their pin, and non-escalating actions that never write a pin
	// (else they'd emit one event per turn). Lookup failure proceeds (best-effort).
	if s.loopEscalationStore != nil && installationID != uuid.Nil {
		count, err := s.loopEscalationStore.CountLoopEscalationEvents(ctx, sessionKey[:], role)
		if err != nil {
			log.Error("loop-escalation: budget lookup failed", "err", err)
		} else if count > 0 {
			return // this session already fired its one escalation event
		}
	}

	// Holdout only applies when the event can be recorded (wired store + real
	// installation id) — otherwise withholding the rescue is pure loss, not measurement.
	holdout := s.loopEscalationStore != nil && installationID != uuid.Nil &&
		DeterministicHoldout(sessionKey, s.ResolveLoopEscalationHoldoutPct(ctx))

	automaticRoutingDisabled := false
	if !userForced {
		_, automaticRoutingDisabled = s.globalAutomaticExclusionReason(ctx, escalateModel)
	}

	action := loopActionEscalated
	switch {
	case !s.ResolveLoopEscalationEnabled(ctx):
		action = loopActionDisabled
	case userForced:
		action = loopActionUserForced
	case automaticRoutingDisabled:
		action = loopActionAutomaticRoutingDisabled
	case loopingModel == escalateModel:
		action = loopActionAlreadyStrong
	case holdout:
		action = loopActionHoldout
	}
	willEscalate := action == loopActionEscalated

	// This (session, looping_model) event is a training label for the
	// difficulty/routing model; joined offline by session_key against the final shard result.
	log.Info("router.loop_escalation",
		"looping_model", loopingModel,
		"action", action,
		"escalated", willEscalate,
		"user_forced", userForced,
		"escalation_target", escalateModel,
		"loop_tool", top.Name,
		"loop_input_hash", top.InputHash,
		"repeat_count", topCount,
		"distinct_ratio", distinctRatio,
		"window_size", window,
		"session_key_prefix", shortSessionKey(sessionKey),
		"role", role,
	)

	// Rescue first, record second: the durable row backs the once-per-session
	// budget, so recording before the pin lands would permanently block retry
	// on a failed rescue. On upsert failure, return without a row so the loop re-detects next turn.
	if willEscalate {
		// Pin opus for the rest of the session (immutable sticky via
		// ReasonLoopEscalation).
		if s.pinStore == nil || installationID == uuid.Nil {
			return
		}
		var lastServed string
		if existing, found, err := s.pinStore.Get(ctx, sessionKey, role); err == nil && found && pinMatchesEffectiveStrategy(ctx, existing) {
			lastServed = existing.LastServedModel
		}
		pin := sessionpin.Pin{
			SessionKey:      sessionKey,
			Role:            role,
			InstallationID:  installationID,
			Provider:        providers.ProviderAnthropic,
			Model:           escalateModel,
			Reason:          translate.ReasonLoopEscalation,
			Strategy:        router.StrategyFromContext(ctx),
			TurnCount:       1,
			PinnedUntil:     time.Now().Add(pinSessionTTL),
			LastServedModel: lastServed,
		}
		// context.Background(): the request ctx may already be canceled by the
		// time this runs; the pin write must still land or the next turn re-loops.
		if err := s.pinStore.Upsert(context.Background(), pin); err != nil {
			log.Error("loop-escalation: pin upsert failed", "err", err)
			return
		}
	}

	// Durable row for fire-rate/opus-share metrics and the training corpus.
	// context.Background(): request ctx may be canceled; losing the row would
	// skew the corpus and break the once-per-session budget (pin check still dedupes re-fires meanwhile).
	if s.loopEscalationStore != nil && installationID != uuid.Nil {
		event := conversation.LoopEscalationEvent{
			InstallationID:   installationID.String(),
			SessionKey:       sessionKey[:],
			Role:             role,
			LoopingModel:     loopingModel,
			Action:           action,
			EscalationTarget: escalateModel,
			LoopTool:         top.Name,
			LoopInputHash:    top.InputHash,
			RepeatCount:      int32(topCount),
			DistinctRatio:    distinctRatio,
			WindowSize:       int32(window),
		}
		if err := s.loopEscalationStore.InsertLoopEscalationEvent(context.Background(), event); err != nil {
			log.Error("loop-escalation: event insert failed", "err", err)
		}
	}
}
