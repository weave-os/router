package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeContinuationPins struct {
	loaded     sessionpin.Pin
	found      bool
	upserted   []sessionpin.Pin
	consumed   []string
	consumePin sessionpin.Pin
	consumeOK  bool
	consumeErr error
}

func (f *fakeContinuationPins) Load(context.Context, [sessionpin.SessionKeyLen]byte, string) (sessionpin.Pin, bool) {
	return f.loaded, f.found
}

func (f *fakeContinuationPins) Upsert(_ context.Context, pin sessionpin.Pin) {
	f.upserted = append(f.upserted, pin)
}

func (f *fakeContinuationPins) Consume(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string, _ router.Strategy) (sessionpin.Pin, bool, error) {
	f.consumed = append(f.consumed, role)
	return f.consumePin, f.consumeOK, f.consumeErr
}

func continuationFor(pins conversation.ContinuationPins) conversation.PostCommandContinuation {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return conversation.PostCommandContinuation{Pins: pins, Now: func() time.Time { return now }}
}

func TestPostCommandContinuationGrantStoresOneShotPin(t *testing.T) {
	pins := &fakeContinuationPins{loaded: sessionpin.Pin{Model: "model-a", Provider: "provider-a", TurnCount: 7}, found: true}
	installationID := uuid.New()

	continuationFor(pins).Grant(context.Background(), installationID, [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole)

	require.Len(t, pins.upserted, 1)
	got := pins.upserted[0]
	assert.Equal(t, conversation.CommandContinuationRole(sessionpin.DefaultRole), got.Role)
	assert.Equal(t, installationID, got.InstallationID)
	assert.Equal(t, 1, got.TurnCount)
	assert.False(t, got.PinnedUntil.IsZero())
}

func TestPostCommandContinuationGrantSkips(t *testing.T) {
	for name, tc := range map[string]struct {
		pins           *fakeContinuationPins
		installationID uuid.UUID
	}{
		"no installation": {pins: &fakeContinuationPins{loaded: sessionpin.Pin{Model: "m", Provider: "p"}, found: true}},
		"no pin":          {pins: &fakeContinuationPins{}, installationID: uuid.New()},
		"no provider":     {pins: &fakeContinuationPins{loaded: sessionpin.Pin{Model: "m"}, found: true}, installationID: uuid.New()},
	} {
		t.Run(name, func(t *testing.T) {
			continuationFor(tc.pins).Grant(context.Background(), tc.installationID, [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole)
			assert.Empty(t, tc.pins.upserted)
		})
	}
}

func TestPostCommandContinuationWithoutPinsIsNoop(t *testing.T) {
	continuation := conversation.PostCommandContinuation{}
	continuation.Grant(context.Background(), uuid.New(), [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole)
	_, found := continuation.Consume(context.Background(), [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole)
	assert.False(t, found)
	assert.NoError(t, continuation.Invalidate(context.Background(), [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole))
}

func TestPostCommandContinuationConsumeAndInvalidateUseContinuationRole(t *testing.T) {
	pins := &fakeContinuationPins{consumeErr: errors.New("store down")}
	continuation := continuationFor(pins)

	_, found := continuation.Consume(context.Background(), [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole)
	assert.False(t, found)
	require.Error(t, continuation.Invalidate(context.Background(), [sessionpin.SessionKeyLen]byte{1}, sessionpin.DefaultRole))

	role := conversation.CommandContinuationRole(sessionpin.DefaultRole)
	assert.Equal(t, []string{role, role}, pins.consumed)
}
