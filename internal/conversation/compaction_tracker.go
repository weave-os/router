package conversation

import (
	"encoding/hex"
	"sync"
	"time"

	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	compactionCacheSize = 4096
	compactionCacheTTL  = 5 * time.Minute
)

type compactionState struct {
	messageCount  int
	toolCallCount int
}

const compactionMinHistoryMessages = 8

type CompactionTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, compactionState]
}

func NewCompactionTracker() *CompactionTracker {
	return &CompactionTracker{
		cache: lru.NewLRU[string, compactionState](compactionCacheSize, nil, compactionCacheTTL),
	}
}

func (t *CompactionTracker) CheckAndRecord(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string, messageCount, toolCallCount int) bool {
	if t == nil {
		return false
	}
	key, ok := compactionBucketKey(sessionKey, installationID, role)
	if !ok {
		return false
	}
	t.mu.Lock()
	last, found := t.cache.Get(key)
	t.cache.Add(key, compactionState{messageCount: messageCount, toolCallCount: toolCallCount})
	t.mu.Unlock()

	if !found {
		return false
	}

	if messageCount < last.messageCount && last.messageCount >= compactionMinHistoryMessages {
		return true
	}

	if toolCallCount < last.toolCallCount && messageCount >= compactionMinHistoryMessages {
		return true
	}
	return false
}

func (t *CompactionTracker) Tracks(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string) bool {
	if t == nil {
		return false
	}
	key, ok := compactionBucketKey(sessionKey, installationID, role)
	if !ok {
		return false
	}
	_, found := t.cache.Get(key)
	return found
}

func compactionBucketKey(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string) (string, bool) {
	if sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		return "session:" + hex.EncodeToString(sessionKey[:]) + ":" + role, true
	}
	if installationID != uuid.Nil {
		return "install:" + installationID.String() + ":" + role, true
	}
	return "", false
}
