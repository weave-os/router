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

// Cross-envelope tool-call loop detection.
//
// loop_detection.go catches loops within one request body. This catches a
// different failure mode: Claude Code's parent agent spawning fresh
// sub-agents that each make one tool call and get an identical empty
// result — each envelope-1 request looks fine on its own, so the
// per-envelope detector misses it.
//
// NoProgressTracker keys an LRU on the session key and rings recent dispatch
// fingerprints; NoProgressMatchThreshold repeats within NoProgressTimeWindow
// mark the session stuck, so the proxy emits a synthetic stop and expires
// the pin.

const (
	NoProgressCacheSize      = 4096
	NoProgressCacheTTL       = 5 * time.Minute
	NoProgressWindowSize     = 8
	NoProgressMatchThreshold = 5
	NoProgressTimeWindow     = 90 * time.Second
	noProgressPromptPrefix   = 512
)

// NoProgressFingerprint identifies one dispatch by model, provider, message
// count, tool-call progress, and a hash of the prompt prefix.
type NoProgressFingerprint [32]byte

// ToolProgressMarker summarizes tool-call progress this turn: count of
// assistant tool calls plus the last call's signature (name + arg hash).
// Empty for tool-free turns or Gemini requests (unparsed). Lets the
// no-progress fingerprint tell a progressing loop (marker changes each turn)
// from a stuck one (marker constant), without relying on top-level message
// count, which Claude Code sub-agents hold flat.
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

// ComputeNoProgressFingerprint hashes routed (model, provider), a tool-call
// progress marker, the prompt prefix, and — only when there is no progress
// marker — the message count.
//
// progressMarker is the primary stuck signal; it stays frozen while the same
// tool call re-issues, so matching fingerprints accumulate. messageCount is
// excluded when a marker is present because retries/continuations still append
// messages even when stuck — folding it in defeats detection. For the tool-free
// case (empty marker) it remains the sole fallback signal.
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

func (r *fingerprintRing) RecordAndDetect(fp NoProgressFingerprint, now time.Time) (looped bool, count int) {
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

// NoProgressTracker is the package-level tracker. Construct via
// NewNoProgressTracker; nil receivers are valid (no-op).
//
// mu serializes the check-then-create of a bucket's ring in RecordAndDetect:
// the LRU synchronizes individual Get/Add calls, but two goroutines racing on
// the first insert can each allocate a ring and silently orphan one (with its
// recorded fingerprint). ring.recordAndDetect runs outside mu — fingerprintRing
// guards its own state — so contention on one bucket never blocks others.
type NoProgressTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, *fingerprintRing]
}

func NewNoProgressTracker() *NoProgressTracker {
	return &NoProgressTracker{
		cache: lru.NewLRU[string, *fingerprintRing](NoProgressCacheSize, nil, NoProgressCacheTTL),
	}
}

// compactionState is per-session state: last-seen message count and
// assistant tool-call count. Either dropping can signal history trimming.
type compactionState struct {
	msgCount      int
	toolCallCount int
}

// compactionMinHistoryMessages is the floor below which a count drop is
// benign, not real trimming: fresh sub-agents open at messageCount=1 (looks
// like a drop vs. a prior sub-agent in the same bucket), and Claude Code's
// Explore sub-agent swings its tool-call count widely on a flat ~3-message
// window. Real compaction/trimming only happens on large conversations,
// well above this floor. Bias toward fewer fires: a missed detection just
// passes the body through unchanged, but a false one corrupts an in-flight
// sub-agent with a lossy summary.
const compactionMinHistoryMessages = 8

// CompactionTracker detects Claude Code context-window trimming by comparing
// each turn's message count and tool-call count to the last seen values,
// which can leave a non-Anthropic model unaware of work done in elided turns.
//
// Two independent signals catch two trimming shapes: full compaction (message
// count drops sharply, e.g. 20→3) and rolling-window trimming (message count
// stays flat while tool-call count shrinks by one per turn, e.g. 9→8→7→6→5).
// Checking only message count misses the rolling-window case.
//
// Both signals are gated by compactionMinHistoryMessages (see that constant)
// to avoid false-firing on sub-agent startup and Explore's flat window.
//
// nil receivers are valid (no-op), matching NoProgressTracker.
//
// mu makes CheckAndRecord's Get+Add a single step: the LRU synchronizes each
// call individually, but a concurrent Get between the read and the write can
// observe stale prior state and false-trip the drop check.
type CompactionTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, compactionState]
}

func NewCompactionTracker() *CompactionTracker {
	return &CompactionTracker{
		cache: lru.NewLRU[string, compactionState](NoProgressCacheSize, nil, NoProgressCacheTTL),
	}
}

// CheckAndRecord records the session's current counts and reports whether
// either dropped vs. the prior observation. Returns false on first
// observation or when no bucket anchor is available.
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
	// Full compaction: messageCount dropped from a substantial prior
	// conversation (gated to reject the fresh-sub-agent-at-1 case).
	if messageCount < last.msgCount && last.msgCount >= compactionMinHistoryMessages {
		return true
	}
	// Rolling-window trimming: messageCount flat, tool-call count shrinks
	// (gated to reject Explore's flat ~3-message window).
	if toolCallCount < last.toolCallCount && messageCount >= compactionMinHistoryMessages {
		return true
	}
	return false
}

// Tracks reports whether the session's counts have been recorded.
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

// RecordAndDetect records the fingerprint in a bucket keyed by sessionKey
// (preferred) or installationID (fallback), and reports whether the burst
// exceeds the loop threshold. nil tracker returns (false, 0).
//
// Bucket selection:
//   - sessionKey set → per-session bucket
//   - sessionKey zero, installationID set → per-installation bucket
//     (hard-pin paths, pinStore nil); coarser, but the fingerprint tuple
//     still distinguishes unrelated work
//   - neither set → skipped, to avoid one global bucket false-tripping
//     across unrelated traffic
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

	return ring.RecordAndDetect(fp, now)
}

// noProgressBucketKey picks the LRU bucket key. ok=false means no anchor is
// available and the caller must skip detection.
func noProgressBucketKey(sessionKey [sessionpin.SessionKeyLen]byte, installationID uuid.UUID, role string) (string, bool) {
	if sessionKey != ([sessionpin.SessionKeyLen]byte{}) {
		return "session:" + hex.EncodeToString(sessionKey[:]) + ":" + role, true
	}
	if installationID != uuid.Nil {
		return "install:" + installationID.String() + ":" + role, true
	}
	return "", false
}
