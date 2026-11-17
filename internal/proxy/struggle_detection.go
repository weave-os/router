package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	struggleEarlyTurns     = 30
	struggleEarlyWall      = 10 * time.Minute
	struggleReasonEarlyStr = "early"

	struggleLateTurns     = 80
	struggleLateWall      = 30 * time.Minute
	struggleReasonLateStr = "late"
)

type StruggleShadowStore interface {
	InsertStruggleShadowEvent(ctx context.Context, p StruggleShadowEvent) error
	CountStruggleShadowEvents(ctx context.Context, sessionKey []byte, role, reason string) (count int64, err error)
}

type StruggleShadowEvent struct {
	InstallationID      string
	SessionKey          []byte
	Role                string
	RoutedModel         string
	TurnType            string
	Reason              string
	TurnCount           int32
	WallSeconds         int64
	SessionEverSwitched bool
	EstInputTokens      int32
}

const (
	struggleFiredCacheSize = 8192
	struggleFiredCacheTTL  = time.Hour
)

type struggleTracker struct {
	fired *lru.LRU[string, struct{}]
}

func newStruggleTracker() *struggleTracker {
	return &struggleTracker{
		fired: lru.NewLRU[string, struct{}](struggleFiredCacheSize, nil, struggleFiredCacheTTL),
	}
}

func struggleFiredKey(sessionKey [sessionpin.SessionKeyLen]byte, role, reason string) string {
	return string(sessionKey[:]) + "\x00" + role + "\x00" + reason
}

func struggleReasons(turnCount int, wall time.Duration) []string {
	if turnCount >= struggleLateTurns && wall >= struggleLateWall {
		return []string{struggleReasonLateStr}
	}
	if turnCount >= struggleEarlyTurns && wall >= struggleEarlyWall {
		return []string{struggleReasonEarlyStr}
	}
	return nil
}

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
	key := struggleFiredKey(sessionKey, role, reason)
	if _, seen := s.struggleTracker.fired.Get(key); seen {
		return
	}

	if s.struggleShadowStore != nil && installationID != uuid.Nil {
		count, err := s.struggleShadowStore.CountStruggleShadowEvents(ctx, sessionKey[:], role, reason)
		if err != nil {
			log.Error("struggle-shadow: budget lookup failed", "err", err)
		} else if count > 0 {
			s.struggleTracker.fired.Add(key, struct{}{})
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
		event := StruggleShadowEvent{
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
		if err := s.struggleShadowStore.InsertStruggleShadowEvent(context.WithoutCancel(ctx), event); err != nil {
			log.Error("struggle-shadow: event insert failed", "err", err)
			return
		}
	}
	s.struggleTracker.fired.Add(key, struct{}{})
}
