package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
	"weave-os/router/internal/auth"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

const (
	noProgressCacheSize      = 4096
	noProgressCacheTTL       = 5 * time.Minute
	noProgressWindowSize     = 8
	noProgressMatchThreshold = 5
	noProgressTimeWindow     = 90 * time.Second
	noProgressPromptPrefix   = 512
)

type noProgressFingerprint [32]byte

func toolProgressMarker(env *translate.RequestEnvelope) string {
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

func computeNoProgressFingerprint(decision router.Decision, promptText string, messageCount int, progressMarker string) noProgressFingerprint {
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
	fp   noProgressFingerprint
	when time.Time
}

type fingerprintRing struct {
	mu      sync.Mutex
	entries []fingerprintEntry
}

func (r *fingerprintRing) recordAndDetect(fp noProgressFingerprint, now time.Time) (looped bool, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, fingerprintEntry{fp: fp, when: now})
	if len(r.entries) > noProgressWindowSize {
		r.entries = r.entries[len(r.entries)-noProgressWindowSize:]
	}
	cutoff := now.Add(-noProgressTimeWindow)
	n := 0
	for _, e := range r.entries {
		if e.fp == fp && !e.when.Before(cutoff) {
			n++
		}
	}
	return n >= noProgressMatchThreshold, n
}

type noProgressTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, *fingerprintRing]
}

func newNoProgressTracker() *noProgressTracker {
	return &noProgressTracker{
		cache: lru.NewLRU[string, *fingerprintRing](noProgressCacheSize, nil, noProgressCacheTTL),
	}
}

type compactionState struct {
	msgCount      int
	toolCallCount int
}

const compactionMinHistoryMessages = 8

type compactionTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, compactionState]
}

func newCompactionTracker() *compactionTracker {
	return &compactionTracker{
		cache: lru.NewLRU[string, compactionState](noProgressCacheSize, nil, noProgressCacheTTL),
	}
}

func (t *compactionTracker) checkAndRecord(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string, messageCount, toolCallCount int) bool {
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

func (t *noProgressTracker) recordAndDetect(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string, fp noProgressFingerprint, now time.Time) (looped bool, count int) {
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

func shortSessionKey(sessionKey [sessionpin.SessionKeyLen]byte) string {
	if sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ""
	}
	return fmt.Sprintf("%x", sessionKey[:8])
}

func (s *Service) handleNoProgressBreak(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	count int,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	decisionModel string,
	decisionProvider string,
	inputTokens int,
	decisionReason ...string,
) error {
	log := observability.FromContext(ctx)
	preserveForcedPin := len(decisionReason) > 0 && isUserForcedReason(decisionReason[0])
	if !preserveForcedPin && s.pinStore != nil && installationID != uuid.Nil && sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		pin, found := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now).Load(ctx, sessionKey, role)
		preserveForcedPin = found && isUserForcedReason(pin.Reason)
	}
	pinAction := "clearing the session pin so the next message re-routes"
	if preserveForcedPin {
		pinAction = "preserving the explicit force-model pin for the next message"
	}

	msg := fmt.Sprintf(
		"✦ **Weave Router** → No-progress loop detected: %d consecutive requests under this session routed to `%s` (`%s`) with no observable progress in %s. Stopping this turn and %s.\n\nIf the task is genuinely incomplete, send a follow-up message describing what's still needed; the router will pick a different model.\n\n",
		count, decisionModel, decisionProvider, noProgressTimeWindow, pinAction,
	)
	if env.SourceFormat() == translate.FormatOpenAI {
		msg = fmt.Sprintf(
			"Weave Router: no-progress loop detected (%d consecutive routes to %s/%s with no progress in %s). Stopping and %s.",
			count, decisionModel, decisionProvider, noProgressTimeWindow, pinAction,
		)
	}

	if _, active := auth.BlindExperimentFrom(ctx); active {
		msg = blindExperimentLoopStopMessage
	}

	log.Info("No-progress loop detected; breaking turn",
		"repeat_count", count,
		"window_size", noProgressWindowSize,
		"time_window", noProgressTimeWindow.String(),
		"decision_model", decisionModel,
		"decision_provider", decisionProvider,
		"session_key_prefix", shortSessionKey(sessionKey),
		"role", role,
	)

	if s.pinStore != nil && installationID != uuid.Nil && sessionKey != ([sessionpin.SessionKeyLen]byte{}) && !preserveForcedPin {
		if err := s.expireSessionPinAndHMMHistory(ctx, installationID, sessionKey, role, "no_progress_loop_break"); err != nil {
			log.Error("no-progress-break: pin store upsert failed", "err", err)
		}
	}

	ack := newCommandAcknowledgment(translate.CommandLoopStop)
	return writeCommandOutcome(ctx, w, env, msg, ack.outcome, inputTokens)
}
