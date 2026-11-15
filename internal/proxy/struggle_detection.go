package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
)

// handleStruggleShadow records one durable event + one log line per (session,
// role, reason). Takes no routing action.
func (s *Service) handleStruggleShadow(
	ctx context.Context,
	reason string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	routedModel string,
	turnType string,
	turnCount int,
	wall time.Duration,
	sessionEverSwitched bool,
	estInputTokens int,
) {
	log := observability.FromContext(ctx)
	key := conversation.StruggleFiredKey(sessionKey, role, reason)
	if s.struggleTracker.Fired(key) {
		return
	}

	// Best-effort: a lookup failure proceeds — an extra row beats a lost one in
	// shadow mode.
	if s.struggleShadowStore != nil && installationID != uuid.Nil {
		count, err := s.struggleShadowStore.CountStruggleShadowEvents(ctx, sessionKey[:], role, reason)
		if err != nil {
			log.Error("struggle-shadow: budget lookup failed", "err", err)
		} else if count > 0 {
			s.struggleTracker.MarkFired(key)
			return
		}
	}

	log.Info("router.struggle_shadow",
		"reason", reason,
		"routed_model", routedModel,
		"turn_type", turnType,
		"turn_count", turnCount,
		"wall_seconds", int64(wall.Seconds()),
		"session_ever_switched", sessionEverSwitched,
		"est_input_tokens", estInputTokens,
		"session_key_prefix", shortSessionKey(sessionKey),
		"role", role,
	)

	if s.struggleShadowStore != nil && installationID != uuid.Nil {
		event := conversation.StruggleShadowEvent{
			InstallationID:      installationID.String(),
			SessionKey:          sessionKey[:],
			Role:                role,
			RoutedModel:         routedModel,
			TurnType:            turnType,
			Reason:              reason,
			TurnCount:           int32(turnCount),
			WallSeconds:         int64(wall.Seconds()),
			SessionEverSwitched: sessionEverSwitched,
			EstInputTokens:      int32(estInputTokens),
		}
		// context.WithoutCancel: the request ctx may already be canceled; losing
		// the row would skew the shadow fire-rate corpus.
		if err := s.struggleShadowStore.InsertStruggleShadowEvent(context.WithoutCancel(ctx), event); err != nil {
			log.Error("struggle-shadow: event insert failed", "err", err)
			return // leave the LRU unset so the next turn retries
		}
	}
	s.struggleTracker.MarkFired(key)
}

// WithStruggleShadowConfig sets the shadow-mode struggle detector kill switch.
// enabled=false skips the check entirely. Shadow mode takes no routing action
// either way; the switch exists to shed the per-turn check if it misbehaves.
func (s *Service) WithStruggleShadowConfig(enabled bool) *Service {
	s.struggleShadowEnabled = enabled
	return s
}

// WithStruggleShadowStore wires the durable sink for shadow struggle events
// (router.struggle_shadow_events). Nil degrades to log-only fires with
// replica-local de-duplication.
func (s *Service) WithStruggleShadowStore(store conversation.StruggleShadowStore) *Service {
	s.struggleShadowStore = store
	return s
}
