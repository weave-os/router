package conversation

import (
	"context"
	"time"

	"weave-os/router/internal/router/sessionpin"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	StruggleEarlyTurns  = 30
	StruggleEarlyWall   = 10 * time.Minute
	StruggleReasonEarly = "early"

	struggleLateTurns  = 80
	struggleLateWall   = 30 * time.Minute
	StruggleReasonLate = "late"
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

type StruggleTracker struct {
	fired *lru.LRU[string, struct{}]
}

func NewStruggleTracker() *StruggleTracker {
	return &StruggleTracker{
		fired: lru.NewLRU[string, struct{}](struggleFiredCacheSize, nil, struggleFiredCacheTTL),
	}
}

func (t *StruggleTracker) Fired(key string) bool {
	_, seen := t.fired.Get(key)
	return seen
}

func (t *StruggleTracker) MarkFired(key string) {
	t.fired.Add(key, struct{}{})
}

func StruggleFiredKey(sessionKey [sessionpin.SessionKeyLen]byte, role, reason string) string {
	return string(sessionKey[:]) + "\x00" + role + "\x00" + reason
}

func StruggleReasons(turnCount int, wall time.Duration) []string {
	if turnCount >= struggleLateTurns && wall >= struggleLateWall {
		return []string{StruggleReasonLate}
	}
	if turnCount >= StruggleEarlyTurns && wall >= StruggleEarlyWall {
		return []string{StruggleReasonEarly}
	}
	return nil
}
