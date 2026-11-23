package sessionpin_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/sessionpin"
)

type recordingFullStore struct {
	sessionpin.FullStore
	upserts int
	gets    int
}

func (r *recordingFullStore) Get(context.Context, [sessionpin.SessionKeyLen]byte, string) (sessionpin.Pin, bool, error) {
	r.gets++
	return sessionpin.Pin{Model: "pinned-model"}, true, nil
}

func (r *recordingFullStore) Upsert(context.Context, sessionpin.Pin) error {
	r.upserts++
	return nil
}

func sessionPinOverride(enabled bool) context.Context {
	return flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeySessionPinEnabled: enabled},
	})
}

func TestGatedByFlag_DisabledRequestSeesNoPinAndWritesNothing(t *testing.T) {
	base := &recordingFullStore{}
	store := sessionpin.GatedByFlag(base)
	ctx := sessionPinOverride(false)

	pin, found, err := store.Get(ctx, [sessionpin.SessionKeyLen]byte{}, "main")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, sessionpin.Pin{}, pin)
	require.NoError(t, store.Upsert(ctx, sessionpin.Pin{Model: "fresh-model"}))
	assert.Zero(t, base.gets)
	assert.Zero(t, base.upserts)
	assert.False(t, sessionpin.Enabled(ctx))
	assert.True(t, sessionpin.Enabled(context.Background()))
}

func TestGatedByFlag_DefaultAndEnabledRequestsUseTheStore(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no override":      context.Background(),
		"explicit enabled": sessionPinOverride(true),
	} {
		t.Run(name, func(t *testing.T) {
			base := &recordingFullStore{}
			store := sessionpin.GatedByFlag(base)

			pin, found, err := store.Get(ctx, [sessionpin.SessionKeyLen]byte{}, "main")
			require.NoError(t, err)
			assert.True(t, found)
			assert.Equal(t, "pinned-model", pin.Model)
			require.NoError(t, store.Upsert(ctx, sessionpin.Pin{Model: "fresh-model"}))
			assert.Equal(t, 1, base.upserts)
		})
	}
}

func TestEnabled_UpstreamGatewayWithoutSessionIsUnpinned(t *testing.T) {
	headerless := requestcontext.WithUpstreamGateway(context.Background(), requestcontext.UpstreamGatewayHeaders{}, http.Header{})
	assert.False(t, sessionpin.Enabled(headerless), "a gateway turn without a session header must not read or write pins")

	withSession := requestcontext.WithUpstreamGateway(context.Background(), requestcontext.UpstreamGatewayHeaders{}, http.Header{requestcontext.ClaudeCodeSessionHeader: {"gateway-session"}})
	assert.True(t, sessionpin.Enabled(withSession))
}
