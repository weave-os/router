package proxy

import (
	"context"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/router"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleTextRepetitionBreak_ZeroSessionKeyWritesNoPin(t *testing.T) {
	for _, tt := range []struct {
		name      string
		key       [sessionpin.SessionKeyLen]byte
		wantWrite bool
	}{
		{name: "zero key", key: [sessionpin.SessionKeyLen]byte{}, wantWrite: false},
		{name: "real key", key: sessionKeyFromString("repeating-session"), wantWrite: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &overwritingPinStore{}
			svc := &Service{hmmServing: hmmPorts(), pinStore: store}
			env, err := translate.ParseAnthropic([]byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"continue"}]}`))
			require.NoError(t, err)

			err = loopBreakForTest(svc, sessionTarget{strategy: router.StrategyFromContext(context.Background()), installationID: uuid.New(), sessionKey: tt.key}, "maximum", "synthetic-model", providers.ProviderAnthropic).textRepetition(context.Background(), httptest.NewRecorder(), env, 3, "synthetic", 1)
			require.NoError(t, err)
			assert.Equal(t, tt.wantWrite, store.found)
		})
	}
}

func TestWithTextRepetitionBreakOnZeroValueService(t *testing.T) {
	svc := (&Service{}).WithTextRepetitionBreak(true)
	assert.True(t, svc.lifecycle.TextRepetitionBreak())
	assert.Nil(t, svc.lifecycle.NoProgress(), "a zero-value Service must not gain a no-progress tracker")
}
