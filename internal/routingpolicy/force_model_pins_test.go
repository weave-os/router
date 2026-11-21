package routingpolicy_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

func TestForceModelPinsSetThenLoadServesTheForcedPin(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	pins := routingpolicy.NewForceModelPins(newMemPinStore(), func() time.Time { return now })
	key := [sessionpin.SessionKeyLen]byte{1}

	require.NoError(t, pins.Set(context.Background(), key, uuid.New(), "claude-opus-4-7", "anthropic", "high", ""))
	pin, found, cleared := pins.Load(context.Background(), key, "", false)

	assert.True(t, found)
	assert.False(t, cleared)
	assert.Equal(t, routingpolicy.ForceModelSessionRole, pin.Role)
	assert.Equal(t, translate.ReasonUserForceModel, pin.Reason)
	assert.Equal(t, routingpolicy.PinNeverExpires, pin.PinnedUntil)
	assert.Empty(t, pin.Strategy)
}

func TestForceModelPinsThreadScopedPinIsClearedForOtherThreads(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	pins := routingpolicy.NewForceModelPins(newMemPinStore(), func() time.Time { return now })
	key := [sessionpin.SessionKeyLen]byte{2}

	require.NoError(t, pins.Set(context.Background(), key, uuid.New(), "claude-opus-4-7", "anthropic", "", "thread-a"))

	_, found, cleared := pins.Load(context.Background(), key, "thread-b", false)
	assert.False(t, found)
	assert.True(t, cleared)
	_, found, _ = pins.Load(context.Background(), key, "thread-a", false)
	assert.True(t, found)
}

func TestForceModelPinsClearWritesTheUnforcedMarker(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	pins := routingpolicy.NewForceModelPins(newMemPinStore(), func() time.Time { return now })
	key := [sessionpin.SessionKeyLen]byte{3}

	require.NoError(t, pins.Clear(context.Background(), uuid.New(), key))
	pin, found, cleared := pins.Load(context.Background(), key, "", false)

	assert.False(t, found)
	assert.True(t, cleared)
	assert.Equal(t, routingpolicy.ReasonUserUnforced, pin.Reason)
}
