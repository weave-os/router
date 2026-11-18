package proxy

import (
	"context"
	"encoding/binary"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

const escalateModel = "claude-opus-5"

const (
	loopActionEscalated                = "escalated"
	loopActionHoldout                  = "holdout"
	loopActionAlreadyStrong            = "already_strong"
	loopActionUserForced               = "user_forced"
	loopActionDisabled                 = "disabled"
	loopActionAutomaticRoutingDisabled = "automatic_routing_disabled"
)

func DeterministicHoldout(sessionKey [sessionpin.SessionKeyLen]byte, pct int) bool {
	if pct <= 0 {
		return false
	}
	if pct >= 100 {
		return true
	}
	return int(binary.BigEndian.Uint32(sessionKey[0:4])%100) < pct
}

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
		} else if found && pinMatchesEffectiveStrategy(ctx, existing) && (!forceModelCleared || !isUserForcedReason(existing.Reason)) {
			if existing.Reason == translate.ReasonLoopEscalation {
				return
			}
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

	if s.loopEscalationStore != nil && installationID != uuid.Nil {
		count, err := s.loopEscalationStore.CountLoopEscalationEvents(ctx, sessionKey[:], role)
		if err != nil {
			log.Error("loop-escalation: budget lookup failed", "err", err)
		} else if count > 0 {
			return
		}
	}

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

	if willEscalate {
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
			PinnedUntil:     s.clock.Now().Add(routingpolicy.PinSessionTTL),
			LastServedModel: lastServed,
		}
		if err := s.pinStore.Upsert(context.WithoutCancel(ctx), pin); err != nil {
			log.Error("loop-escalation: pin upsert failed", "err", err)
			return
		}
	}

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
		if err := s.loopEscalationStore.InsertLoopEscalationEvent(context.WithoutCancel(ctx), event); err != nil {
			log.Error("loop-escalation: event insert failed", "err", err)
		}
	}
}

func (s *Service) WithLoopEscalationConfig(enabled bool, holdoutPct int) *Service {
	s.loopEscalationEnabled = enabled
	if holdoutPct < 0 {
		holdoutPct = 0
	}
	if holdoutPct > 100 {
		holdoutPct = 100
	}
	s.loopEscalationHoldoutPct = holdoutPct
	return s
}

func (s *Service) WithLoopEscalationStore(store conversation.LoopEscalationStore) *Service {
	s.loopEscalationStore = store
	return s
}
