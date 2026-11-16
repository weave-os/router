package proxy

import (
	"context"
	"encoding/binary"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

const escalateModel = "claude-opus-5"

type LoopEscalationStore interface {
	InsertLoopEscalationEvent(ctx context.Context, p LoopEscalationEvent) error
	CountLoopEscalationEvents(ctx context.Context, sessionKey []byte, role string) (count int64, err error)
}

type LoopEscalationEvent struct {
	InstallationID   string
	SessionKey       []byte
	Role             string
	LoopingModel     string
	Action           string
	EscalationTarget string
	LoopTool         string
	LoopInputHash    string
	RepeatCount      int32
	DistinctRatio    float64
	WindowSize       int32
}

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

const (
	cyclicLoopWindowSize       = 30
	cyclicLoopMinCalls         = 24
	cyclicLoopMaxDistinctRatio = 0.4
)

var editToolNames = map[string]struct{}{
	"Edit": {}, "Write": {}, "MultiEdit": {}, "NotebookEdit": {},
}

func isCodexPollingCall(args string) bool {
	return strings.HasPrefix(args, "exec:") &&
		strings.Contains(args, "write_stdin") &&
		strings.Contains(args, `\"chars\":\"\"`)
}

func detectCyclicToolCallLoop(env *translate.RequestEnvelope) (looped bool, top translate.ToolCallSig, topCount int, distinctRatio float64, total int) {
	sigs := env.AssistantToolCallSignatures()
	if len(sigs) < cyclicLoopMinCalls {
		return false, translate.ToolCallSig{}, 0, 0, 0
	}
	start := 0
	if len(sigs) > cyclicLoopWindowSize {
		start = len(sigs) - cyclicLoopWindowSize
	}
	window := sigs[start:]
	args := env.AssistantToolCallArgsPreview(start, 200)
	counts := make(map[string]int, len(window))
	keys := make(map[string]translate.ToolCallSig, len(window))
	nonPollingCount := 0
	for i, s := range window {
		if i < len(args) && isCodexPollingCall(args[i]) {
			continue
		}
		nonPollingCount++
		if _, isEdit := editToolNames[s.Name]; isEdit {
			return false, translate.ToolCallSig{}, 0, 0, len(window)
		}
		key := s.Name + "\x00" + s.InputHash
		counts[key]++
		keys[key] = s
	}
	if nonPollingCount < cyclicLoopMinCalls {
		return false, translate.ToolCallSig{}, 0, 0, len(window)
	}
	distinctRatio = float64(len(counts)) / float64(nonPollingCount)
	if distinctRatio >= cyclicLoopMaxDistinctRatio {
		return false, translate.ToolCallSig{}, 0, distinctRatio, len(window)
	}
	for k, c := range counts {
		if c > topCount {
			topCount, top = c, keys[k]
		}
	}
	return true, top, topCount, distinctRatio, len(window)
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
			PinnedUntil:     time.Now().Add(pinSessionTTL),
			LastServedModel: lastServed,
		}
		if err := s.pinStore.Upsert(context.WithoutCancel(ctx), pin); err != nil {
			log.Error("loop-escalation: pin upsert failed", "err", err)
			return
		}
	}

	if s.loopEscalationStore != nil && installationID != uuid.Nil {
		event := LoopEscalationEvent{
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
