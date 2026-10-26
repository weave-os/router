package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

func TestUnreliableIdentityNeverDerivesAutomaticKey(t *testing.T) {
	envelope, err := translate.ParseAnthropic([]byte(`{"metadata":{"user_id":"synthetic-session"},"messages":[{"role":"user","content":"synthetic prompt"}]}`))
	require.NoError(t, err)
	for _, client := range []string{ClientAppCodex, ClientAppClaudeCode, ClientAppOpencode, ClientAppCursor, ClientAppGeminiCLI, "pi", "unknown"} {
		t.Run(client, func(t *testing.T) {
			ctx := requestcontext.WithClientIdentity(context.Background(), ClientIdentity{ClientApp: client, SessionID: "shared-session"})
			scope := automaticStateScopeForRequest(ctx, envelope, "synthetic-key")
			require.False(t, scope.Persistent)
			require.Zero(t, scope.Key)
			require.NotZero(t, deriveForceModelSessionKeyForRequest(ctx, envelope, "synthetic-key", scope.Key))
		})
	}
}

func TestThreadScopeSurvivesCompactionAndIsolatesOwnership(t *testing.T) {
	headers := make(http.Header)
	headers.Set(requestcontext.ConversationIDHeader, "synthetic-root")
	headers.Set(requestcontext.ThreadIDHeader, "synthetic-child")
	ctx := requestcontext.WithClientIdentity(context.Background(), ClientIdentityFromHeaders(headers))
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
	before, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":"original"}]}`))
	require.NoError(t, err)
	after, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":"compacted history"}]}`))
	require.NoError(t, err)
	key := deriveSessionKeyForRequest(ctx, before, "synthetic-key")
	require.NotZero(t, key)
	require.Equal(t, key, deriveSessionKeyForRequest(ctx, after, "synthetic-key"))
	require.NotEqual(t, key, deriveSessionKeyForRequest(ctx, after, "other-key"))
	otherInstallation := context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
	require.NotEqual(t, key, deriveSessionKeyForRequest(otherInstallation, after, "synthetic-key"))
	managed := requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{StateNamespace: "release-one", CredentialIdentity: "credential"})
	rebound := requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{StateNamespace: "release-two", CredentialIdentity: "credential"})
	require.NotEqual(t, deriveSessionKeyForRequest(managed, after, "synthetic-key"), deriveSessionKeyForRequest(rebound, after, "synthetic-key"))
	control := deriveForceModelSessionKeyForRequest(ctx, before, "synthetic-key", key)
	require.Equal(t, requestcontext.ConversationKey("synthetic-key", "synthetic-root", requestcontext.ForceModelConversationKey, [16]byte{}), control)
	require.Zero(t, deriveForceModelSessionKeyForRequest(context.Background(), nil, "synthetic-key", key))
}
