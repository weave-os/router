package conversation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type fakeLoopBreakPins struct {
	pin     sessionpin.Pin
	found   bool
	expired []string
	err     error
}

func (f *fakeLoopBreakPins) Load(context.Context, [sessionpin.SessionKeyLen]byte, string) (sessionpin.Pin, bool) {
	return f.pin, f.found
}

func (f *fakeLoopBreakPins) ExpireWithHistory(_ context.Context, _ uuid.UUID, _ [sessionpin.SessionKeyLen]byte, role, reason string) error {
	f.expired = append(f.expired, role+":"+reason)
	return f.err
}

func loopBreakEnv(t *testing.T) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"retry"}]}`))
	require.NoError(t, err)
	return env
}

func TestShortSessionKeyTruncatesAndRedactsZero(t *testing.T) {
	assert.Empty(t, conversation.ShortSessionKey([sessionpin.SessionKeyLen]byte{}))

	var key [sessionpin.SessionKeyLen]byte
	copy(key[:], "0123456789ABCDEFsomemore")
	got := conversation.ShortSessionKey(key)
	assert.Len(t, got, 16)
	assert.NotContains(t, got, "somemore")
}

func TestBreakNoProgressPinHandling(t *testing.T) {
	key := [sessionpin.SessionKeyLen]byte{1}
	for _, tt := range []struct {
		name        string
		pins        *fakeLoopBreakPins
		key         [sessionpin.SessionKeyLen]byte
		reason      []string
		wantExpired bool
		wantAction  string
	}{
		{name: "expires automatic pin", pins: &fakeLoopBreakPins{}, key: key, wantExpired: true, wantAction: "clearing the session pin"},
		{name: "preserves stored forced pin", pins: &fakeLoopBreakPins{pin: sessionpin.Pin{Reason: translate.ReasonUserForceModel}, found: true}, key: key, wantAction: "preserving the explicit force-model pin"},
		{name: "preserves forced decision", pins: &fakeLoopBreakPins{}, key: key, reason: []string{translate.ReasonUserForceModel}, wantAction: "preserving the explicit force-model pin"},
		{name: "zero key writes nothing", pins: &fakeLoopBreakPins{}, wantAction: "clearing the session pin"},
		{name: "expiry error still stops", pins: &fakeLoopBreakPins{err: errors.New("synthetic")}, key: key, wantExpired: true, wantAction: "clearing the session pin"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := conversation.LoopBreak{Pins: tt.pins, InstallationID: uuid.New(), SessionKey: tt.key, Role: "maximum", DecisionModel: "synthetic-model", DecisionProvider: "synthetic-provider"}.NoProgress(context.Background(), loopBreakEnv(t), 5, tt.reason...)
			assert.Contains(t, msg, tt.wantAction)
			assert.Contains(t, msg, "synthetic-model")
			if tt.wantExpired {
				assert.Equal(t, []string{"maximum:no_progress_loop_break"}, tt.pins.expired)
			} else {
				assert.Empty(t, tt.pins.expired)
			}
		})
	}
}

func TestBreakNoProgressWithoutPinStore(t *testing.T) {
	msg := conversation.LoopBreak{InstallationID: uuid.New(), SessionKey: [sessionpin.SessionKeyLen]byte{1}, Role: "maximum", DecisionModel: "synthetic-model", DecisionProvider: "synthetic-provider"}.NoProgress(context.Background(), loopBreakEnv(t), 5)
	assert.Contains(t, msg, "clearing the session pin")
}

func TestBreakTextRepetitionExpiresOnlyKeyedSessions(t *testing.T) {
	for _, tt := range []struct {
		name        string
		key         [sessionpin.SessionKeyLen]byte
		wantExpired bool
	}{
		{name: "zero key", wantExpired: false},
		{name: "real key", key: [sessionpin.SessionKeyLen]byte{1}, wantExpired: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pins := &fakeLoopBreakPins{}
			msg := conversation.LoopBreak{Pins: pins, InstallationID: uuid.New(), SessionKey: tt.key, Role: "maximum", DecisionModel: "synthetic-model", DecisionProvider: "synthetic-provider"}.TextRepetition(context.Background(), loopBreakEnv(t), 3, "synthetic")
			assert.Contains(t, msg, "repeated the same response 3 times")
			assert.Equal(t, tt.wantExpired, len(pins.expired) == 1)
		})
	}
}
