package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

func TestCodexSystemFirstThreadIsolation(t *testing.T) {
	envelope, err := translate.ParseOpenAI([]byte(`{"messages":[{"role":"system","content":"Synthetic repository instructions"},{"role":"user","content":"Review the same task"}]}`))
	require.NoError(t, err)
	keys := make(map[[16]byte]bool)
	for _, thread := range []string{"parent", "child-one", "child-two", "inherited-fork"} {
		headers := make(http.Header)
		headers.Set("X-App", ClientAppCodex)
		headers.Set("Session-Id", "synthetic-conversation")
		headers.Set("X-Weave-Thread-Id", thread)
		ctx := requestcontext.WithClientIdentity(context.Background(), ClientIdentityFromHeaders(headers))
		key := deriveSessionKeyForRequest(ctx, envelope, "synthetic-key")
		require.NotEqual(t, [16]byte{}, key)
		require.False(t, keys[key], "thread %s shares automatic state with another lifecycle", thread)
		keys[key] = true
		require.Equal(t, key, deriveSessionKeyForRequest(ctx, envelope, "synthetic-key"))
	}
}
