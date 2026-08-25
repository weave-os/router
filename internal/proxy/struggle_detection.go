package proxy

import (
	"context"
	"time"

	"workweave/router/internal/observability"
	"workweave/router/internal/router/sessionpin"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// Shadow-mode struggle detector: flags sessions that grind through varied,
// technically-valid tool calls without converging — a shape the per-turn
// detectors miss (they need literal repetition). LOG ONLY — one durable event
// per operating point per session; joined offline against telemetry to pick an
// operating point before arming.
const (
	// struggleReasonEarly is the cheap operating point: sessions where users
	// most often bail. Arms the escalation move — the next cluster up, or a
	// same-cluster "sideways" arm when nothing above can serve the session.
	struggleEarlyTurns     = 30
	struggleEarlyWall      = 10 * time.Minute
	struggleReasonEarlyStr = "early"

	// struggleReasonLate is the high-precision operating point: strong human-
	// takeover enrichment (Phase 0 measured ~97x over baseline). Reserved for
	// the expensive "up" move (next cluster).
	struggleLateTurns     = 80
	struggleLateWall      = 30 * time.Minute
	struggleReasonLateStr = "late"
)

// StruggleShadowStore persists shadow struggle detections
// (router.struggle_shadow_events). CountStruggleShadowEvents enforces the
// once-per-(session, role, reason) budget across replicas.
type StruggleShadowStore interface {
	InsertStruggleShadowEvent(ctx context.Context, p StruggleShadowEvent) error
	CountStruggleShadowEvents(ctx context.Context, sessionKey []byte, role, reason string) (count int64, err error)
}

// StruggleShadowEvent mirrors one router.struggle_shadow_events row. All signal
// values are recorded regardless of which reason fired, so thresholds can be
// re-tuned offline without re-running traffic.
type StruggleShadowEvent struct {
	InstallationID string
	SessionKey     []byte
	Role           string
	RoutedModel    string
	TurnType       string
	Reason         string
	TurnCount      int32
	// WallSeconds is the session's age at fire time (now - pin.FirstPinnedAt).
	WallSeconds int64
	// SessionEverSwitched distinguishes a session that has already churned
	// models (capacity rotation, provider strikes) from one that has not.
	SessionEverSwitched bool
	// EstInputTokens is the firing turn's estimated inbound prompt size.
	EstInputTokens int32
}

// struggleFiredCache de-dupes per (session, role, reason) per replica;
// cross-replica dupes are resolved offline.
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

// struggleReasons returns the highest operating point crossed, or nil.
// Late supersedes early so each escalation stage maps to a distinct row.
// A missing pin yields zero values that fall below every threshold.
func struggleReasons(turnCount int, wall time.Duration) []string {
	if turnCount >= struggleLateTurns && wall >= struggleLateWall {
		return []string{struggleReasonLateStr}
	}
	if turnCount >= struggleEarlyTurns && wall >= struggleEarlyWall {
		return []string{struggleReasonEarlyStr}
	}
	return nil
}

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
	key := struggleFiredKey(sessionKey, role, reason)
	if _, seen := s.struggleTracker.fired.Get(key); seen {
		return
	}

	// Best-effort: a lookup failure proceeds — an extra row beats a lost one in
	// shadow mode.
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
		// context.Background(): the request ctx may already be canceled; losing
		// the row would skew the shadow fire-rate corpus.
		if err := s.struggleShadowStore.InsertStruggleShadowEvent(context.Background(), event); err != nil {
			log.Error("struggle-shadow: event insert failed", "err", err)
			return // leave the LRU unset so the next turn retries
		}
	}
	s.struggleTracker.fired.Add(key, struct{}{})
}
