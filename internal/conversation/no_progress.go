package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	NoProgressCacheSize      = 4096
	NoProgressCacheTTL       = 5 * time.Minute
	NoProgressWindowSize     = 8
	NoProgressMatchThreshold = 5
	NoProgressTimeWindow     = 90 * time.Second
	noProgressPromptPrefix   = 512
)

type NoProgressFingerprint [32]byte

func ToolProgressMarker(env *translate.RequestEnvelope) string {
	if env == nil {
		return ""
	}
	sigs := env.AssistantToolCallSignatures()
	if len(sigs) == 0 {
		return ""
	}
	last := sigs[len(sigs)-1]
	return strconv.Itoa(len(sigs)) + "\x00" + last.Name + "\x00" + last.InputHash
}

func ComputeNoProgressFingerprint(decision router.Decision, promptText string, messageCount int, progressMarker string) NoProgressFingerprint {
	p := promptText
	if len(p) > noProgressPromptPrefix {
		p = p[:noProgressPromptPrefix]
	}
	countPart := ""
	if progressMarker == "" {
		countPart = strconv.Itoa(messageCount)
	}
	return sha256.Sum256([]byte(decision.Model + "\x00" + decision.Provider + "\x00" + countPart + "\x00" + progressMarker + "\x00" + p))
}

type fingerprintEntry struct {
	fp   NoProgressFingerprint
	when time.Time
}

type fingerprintRing struct {
	mu      sync.Mutex
	entries []fingerprintEntry
}

func (r *fingerprintRing) recordAndDetect(fp NoProgressFingerprint, now time.Time) (looped bool, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, fingerprintEntry{fp: fp, when: now})
	if len(r.entries) > NoProgressWindowSize {
		r.entries = r.entries[len(r.entries)-NoProgressWindowSize:]
	}
	cutoff := now.Add(-NoProgressTimeWindow)
	n := 0
	for _, e := range r.entries {
		if e.fp == fp && !e.when.Before(cutoff) {
			n++
		}
	}
	return n >= NoProgressMatchThreshold, n
}

type NoProgressTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, *fingerprintRing]
}

func NewNoProgressTracker() *NoProgressTracker {
	return &NoProgressTracker{
		cache: lru.NewLRU[string, *fingerprintRing](NoProgressCacheSize, nil, NoProgressCacheTTL),
	}
}

type compactionState struct {
	msgCount      int
	toolCallCount int
}

const compactionMinHistoryMessages = 8

type CompactionTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, compactionState]
}

func NewCompactionTracker() *CompactionTracker {
	return &CompactionTracker{
		cache: lru.NewLRU[string, compactionState](NoProgressCacheSize, nil, NoProgressCacheTTL),
	}
}

func (t *CompactionTracker) CheckAndRecord(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string, messageCount, toolCallCount int) bool {
	if t == nil || t.cache == nil {
		return false
	}
	key, ok := noProgressBucketKey(sessionKey, installationID, role)
	if !ok {
		return false
	}
	t.mu.Lock()
	last, found := t.cache.Get(key)
	t.cache.Add(key, compactionState{msgCount: messageCount, toolCallCount: toolCallCount})
	t.mu.Unlock()

	if !found {
		return false
	}

	if messageCount < last.msgCount && last.msgCount >= compactionMinHistoryMessages {
		return true
	}

	if toolCallCount < last.toolCallCount && messageCount >= compactionMinHistoryMessages {
		return true
	}
	return false
}

func (t *CompactionTracker) Tracks(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string) bool {
	if t == nil || t.cache == nil {
		return false
	}
	key, ok := noProgressBucketKey(sessionKey, installationID, role)
	if !ok {
		return false
	}
	_, found := t.cache.Get(key)
	return found
}

func (t *NoProgressTracker) RecordAndDetect(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string, fp NoProgressFingerprint, now time.Time) (looped bool, count int) {
	if t == nil || t.cache == nil {
		return false, 0
	}
	key, ok := noProgressBucketKey(sessionKey, installationID, role)
	if !ok {
		return false, 0
	}
	t.mu.Lock()
	ring, ringOk := t.cache.Get(key)
	if !ringOk || ring == nil {
		ring = &fingerprintRing{}
		t.cache.Add(key, ring)
	}
	t.mu.Unlock()

	return ring.recordAndDetect(fp, now)
}

func noProgressBucketKey(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string) (string, bool) {
	if sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		return "session:" + hex.EncodeToString(sessionKey[:]) + ":" + role, true
	}
	if installationID != uuid.Nil {
		return "install:" + installationID.String() + ":" + role, true
	}
	return "", false
}
