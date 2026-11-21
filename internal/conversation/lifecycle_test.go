package conversation_test

import (
	"testing"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestLifecycleOwnsCompactionTracker(t *testing.T) {
	lifecycle := conversation.NewLifecycle()
	key := [sessionpin.SessionKeyLen]byte{1}
	installation := uuid.New()

	assert.NotNil(t, lifecycle.Compaction())
	lifecycle.Compaction().CheckAndRecord(key, installation, "main", 9, 0)
	assert.True(t, lifecycle.Compaction().Tracks(key, installation, "main"))
}

func TestZeroLifecycleHasNoTrackers(t *testing.T) {
	var nilLifecycle *conversation.Lifecycle
	assert.Nil(t, nilLifecycle.Compaction())
	assert.Nil(t, nilLifecycle.NoProgress())
	assert.False(t, nilLifecycle.TextRepetitionBreak())
	assert.Nil(t, (&conversation.Lifecycle{}).Compaction())
}
