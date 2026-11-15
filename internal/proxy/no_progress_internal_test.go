package proxy

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sessionKeyFromString(s string) [sessionpin.SessionKeyLen]byte {
	var k [sessionpin.SessionKeyLen]byte
	copy(k[:], []byte(s))
	return k
}

func TestHandleNoProgressBreak_PreservesUserForcedPin(t *testing.T) {
	key := sessionKeyFromString("forced-session")
	store := &overwritingPinStore{
		pin: sessionpin.Pin{
			Provider:    "anthropic",
			Model:       "claude-opus-4-8",
			Reason:      translate.ReasonUserForceModel,
			PinnedUntil: time.Now().Add(time.Hour),
		},
		found: true,
	}
	svc := &Service{pinStore: store}
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"retry"}]}`))
	require.NoError(t, err)
	rec := httptest.NewRecorder()

	err = svc.handleNoProgressBreak(
		context.Background(), rec, env, conversation.NoProgressMatchThreshold, uuid.New(), key,
		"default_high", "claude-opus-4-8", "anthropic", 10,
	)
	require.NoError(t, err)

	assert.Equal(t, translate.ReasonUserForceModel, store.pin.Reason,
		"automatic no-progress recovery must not clear an explicit force-model pin")
	assert.Contains(t, rec.Body.String(), "preserving the explicit force-model pin")
}

func TestShortSessionKey_TruncatesAndRedactsZero(t *testing.T) {
	var zero [sessionpin.SessionKeyLen]byte
	assert.Empty(t, shortSessionKey(zero), "zero key must produce empty so logs distinguish missing-anchor from real")

	key := sessionKeyFromString("0123456789ABCDEFsomemore")
	got := shortSessionKey(key)
	assert.Len(t, got, 16, "must log only the first 8 bytes (16 hex chars) to limit cross-request correlation")
	assert.NotContains(t, got, "somemore", "tail bytes must not appear in logs")
}
