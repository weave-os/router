package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/router"

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
	svc := &Service{hmmServing: hmmPorts(), pinStore: store}
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"retry"}]}`))
	require.NoError(t, err)
	rec := httptest.NewRecorder()

	err = loopBreakForTest(svc, sessionTarget{strategy: router.StrategyFromContext(context.Background()), installationID: uuid.New(), sessionKey: key}, "default_high", "claude-opus-4-8", "anthropic").noProgress(context.Background(), rec, env, conversation.NoProgressMatchThreshold, 10)

	require.NoError(t, err)

	assert.Equal(t, translate.ReasonUserForceModel, store.pin.Reason,
		"automatic no-progress recovery must not clear an explicit force-model pin")
	assert.Contains(t, rec.Body.String(), "preserving the explicit force-model pin")
}

func TestHandleNoProgressBreak_ZeroSessionKeyWritesNoPin(t *testing.T) {
	store := &overwritingPinStore{}
	svc := &Service{hmmServing: hmmPorts(), pinStore: store}
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"retry"}]}`))
	require.NoError(t, err)

	err = loopBreakForTest(svc, sessionTarget{strategy: router.StrategyFromContext(context.Background()), installationID: uuid.New(), sessionKey: [sessionpin.SessionKeyLen]byte{}}, "default_high", "claude-opus-4-8", "anthropic").noProgress(context.Background(), httptest.NewRecorder(), env, conversation.NoProgressMatchThreshold, 10)

	require.NoError(t, err)
	assert.False(t, store.found, "a zero-keyed pin row would be shared by every zero-keyed session")
}

func TestHandleNoProgressBreak_ExpiresNonZeroSessionPin(t *testing.T) {
	store := &overwritingPinStore{}
	svc := &Service{hmmServing: hmmPorts(), pinStore: store}
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"retry"}]}`))
	require.NoError(t, err)

	err = loopBreakForTest(svc, sessionTarget{strategy: router.StrategyFromContext(context.Background()), installationID: uuid.New(), sessionKey: sessionKeyFromString("stuck-session")}, "default_high", "claude-opus-4-8", "anthropic").noProgress(context.Background(), httptest.NewRecorder(), env, conversation.NoProgressMatchThreshold, 10)

	require.NoError(t, err)
	assert.True(t, store.found)
}

type loopBreakCall struct {
	stop conversation.LoopBreak
}

func loopBreakForTest(svc *Service, target sessionTarget, role, decisionModel, decisionProvider string) loopBreakCall {
	return loopBreakCall{stop: conversation.LoopBreak{
		Pins: newLoopBreakPins(svc, target.strategy), InstallationID: target.installationID, SessionKey: target.sessionKey,
		Role: role, DecisionModel: decisionModel, DecisionProvider: decisionProvider,
	}}
}

func (c loopBreakCall) noProgress(ctx context.Context, w http.ResponseWriter, env *translate.RequestEnvelope, count, inputTokens int, decisionReason ...string) error {
	return writeLoopStop(ctx, w, env, c.stop.NoProgress(ctx, env, count, decisionReason...), inputTokens)
}

func (c loopBreakCall) textRepetition(ctx context.Context, w http.ResponseWriter, env *translate.RequestEnvelope, count int, sampleHash string, inputTokens int) error {
	return writeLoopStop(ctx, w, env, c.stop.TextRepetition(ctx, env, count, sampleHash), inputTokens)
}
