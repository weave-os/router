package conversation

import (
	"testing"

	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func sessionKeyFromString(s string) [sessionpin.SessionKeyLen]byte {
	var k [sessionpin.SessionKeyLen]byte
	copy(k[:], []byte(s))
	return k
}

func TestCompactionTracker_DetectsMessageCountDrop(t *testing.T) {
	// Full compaction: messageCount drops sharply, tool-call count also drops.
	ct := NewCompactionTracker()
	key := sessionKeyFromString("session-compaction")
	install := uuid.New()

	// First observation: no prior — must return false.
	assert.False(t, ct.CheckAndRecord(key, install, "high", 20, 9), "first observation must not report compaction")

	// Both counts grow: progressing session.
	assert.False(t, ct.CheckAndRecord(key, install, "high", 22, 10), "growing counts must not report compaction")

	// messageCount drops sharply (full compaction): fire.
	assert.True(t, ct.CheckAndRecord(key, install, "high", 3, 0), "sharp messageCount drop must be reported as compaction")

	// Counts stable after compaction.
	assert.False(t, ct.CheckAndRecord(key, install, "high", 5, 2), "growing counts after compaction must not report compaction")
}

func TestCompactionTracker_DetectsRollingWindowTrimming(t *testing.T) {
	// Rolling-window trim: messageCount stays flat (old pairs swapped for new)
	// but tool-call count shrinks as the oldest call drops out of view.
	ct := NewCompactionTracker()
	key := sessionKeyFromString("session-rolling")
	install := uuid.New()

	// Establish baseline: 10 messages, 9 tool calls.
	ct.CheckAndRecord(key, install, "high", 10, 9)

	// messageCount flat (10→10), toolCallCount drops (9→8): fire.
	assert.True(t, ct.CheckAndRecord(key, install, "high", 10, 8),
		"flat messageCount + shrinking toolCallCount must be detected as rolling-window trim")
}

func TestCompactionTracker_NoFalsePositiveOnFreshSubAgentDispatch(t *testing.T) {
	// A new sub-agent shares the (session, role) bucket with the prior one, so
	// its opening messageCount=1 looks like a drop — but it's a fresh dispatch,
	// not trimming, and must not fire.
	ct := NewCompactionTracker()
	key := sessionKeyFromString("session-subagent")
	install := uuid.New()

	// Prior sub-agent left the bucket at a small count.
	ct.CheckAndRecord(key, install, "low", 3, 2)

	// Fresh sub-agent dispatch opens at messageCount=1, tool-call count 0.
	assert.False(t, ct.CheckAndRecord(key, install, "low", 1, 0),
		"fresh sub-agent dispatch (small prior, mc=1) must not be reported as compaction")
}

func TestCompactionTracker_NoFalsePositiveOnExploreToolCallSwing(t *testing.T) {
	// Explore holds a flat ~3-message window with widely varying tool-call
	// counts; a decrease here is natural, not trimming, and must not fire.
	ct := NewCompactionTracker()
	key := sessionKeyFromString("session-explore")
	install := uuid.New()

	ct.CheckAndRecord(key, install, "low", 3, 11)
	assert.False(t, ct.CheckAndRecord(key, install, "low", 3, 6),
		"flat small-window tool-call decrease (Explore) must not be reported as trimming")
	assert.False(t, ct.CheckAndRecord(key, install, "low", 3, 3),
		"further small-window tool-call decrease must not be reported as trimming")
}

func TestCompactionTracker_NoFalsePositiveWhenBothGrow(t *testing.T) {
	ct := NewCompactionTracker()
	key := sessionKeyFromString("session-growing")
	install := uuid.New()

	ct.CheckAndRecord(key, install, "high", 10, 5)
	assert.False(t, ct.CheckAndRecord(key, install, "high", 12, 6), "both counts growing must not fire")
	assert.False(t, ct.CheckAndRecord(key, install, "high", 14, 6), "flat toolCallCount (no new tool call) must not fire")
}

func TestCompactionTracker_NilReceiverIsNoOp(t *testing.T) {
	var ct *CompactionTracker
	key := sessionKeyFromString("session-nil")
	assert.False(t, ct.CheckAndRecord(key, uuid.New(), "high", 5, 3))
}

func TestCompactionTracker_ZeroAnchorsSkipped(t *testing.T) {
	ct := NewCompactionTracker()
	var zero [sessionpin.SessionKeyLen]byte
	// No session key, no installation — must skip rather than bucket globally.
	ct.CheckAndRecord(zero, uuid.Nil, "high", 10, 5)
	assert.False(t, ct.CheckAndRecord(zero, uuid.Nil, "high", 5, 3), "no anchor → compaction detection must be skipped")
}

func TestCompactionTracker_SeparateSessionsAreIsolated(t *testing.T) {
	ct := NewCompactionTracker()
	install := uuid.New()
	keyA := sessionKeyFromString("session-A")
	keyB := sessionKeyFromString("session-B")

	ct.CheckAndRecord(keyA, install, "high", 20, 9)
	ct.CheckAndRecord(keyB, install, "high", 10, 5)

	// Drop on A must not affect B's baseline.
	assert.True(t, ct.CheckAndRecord(keyA, install, "high", 5, 3), "drop on A must be detected")
	assert.False(t, ct.CheckAndRecord(keyB, install, "high", 12, 6), "B growing from 10→12 must not be compaction")
}
