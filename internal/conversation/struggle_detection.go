package conversation

import (
	"context"
	"time"

	"weave-os/router/internal/router/sessionpin"

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
	StruggleEarlyTurns     = 30
	StruggleEarlyWall      = 10 * time.Minute
	StruggleReasonEarlyStr = "early"

	// struggleReasonLate is the high-precision operating point: strong human-
	// takeover enrichment (Phase 0 measured ~97x over baseline). Reserved for
	// the expensive "up" move (next cluster).
	struggleLateTurns     = 80
	struggleLateWall      = 30 * time.Minute
	StruggleReasonLateStr = "late"
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

// StruggleTracker remembers which StruggleFiredKey values already fired on this
// replica.
type StruggleTracker struct {
	fired *lru.LRU[string, struct{}]
}

func NewStruggleTracker() *StruggleTracker {
	return &StruggleTracker{
		fired: lru.NewLRU[string, struct{}](struggleFiredCacheSize, nil, struggleFiredCacheTTL),
	}
}

// Fired reports whether key was marked on this replica within the cache TTL.
func (t *StruggleTracker) Fired(key string) bool {
	_, seen := t.fired.Get(key)
	return seen
}

// MarkFired records that key fired.
func (t *StruggleTracker) MarkFired(key string) {
	t.fired.Add(key, struct{}{})
}

// StruggleFiredKey identifies one (session, role, reason) fire.
func StruggleFiredKey(sessionKey [sessionpin.SessionKeyLen]byte, role, reason string) string {
	return string(sessionKey[:]) + "\x00" + role + "\x00" + reason
}

// StruggleReasons returns the highest operating point crossed, or nil.
// Late supersedes early so each escalation stage maps to a distinct row.
// A missing pin yields zero values that fall below every threshold.
func StruggleReasons(turnCount int, wall time.Duration) []string {
	if turnCount >= struggleLateTurns && wall >= struggleLateWall {
		return []string{StruggleReasonLateStr}
	}
	if turnCount >= StruggleEarlyTurns && wall >= StruggleEarlyWall {
		return []string{StruggleReasonEarlyStr}
	}
	return nil
}
