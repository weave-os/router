package proxy_test

import (
	"context"
	"github.com/google/uuid"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

func statefulTestContext() context.Context {
	ctx := requestcontext.WithClientIdentity(context.Background(), proxy.ClientIdentity{SessionID: "synthetic-test-conversation"})
	ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "key-1")
	return requestcontext.WithThreadIdentity(ctx, requestcontext.ThreadIdentity{ConversationID: "synthetic-test-conversation", ThreadID: "synthetic-test-thread", Source: requestcontext.ThreadSourceExplicit, Status: requestcontext.ThreadIdentityAvailable})
}

func statefulTestSessionKey(ctx context.Context, env *translate.RequestEnvelope, credential string) [16]byte {
	identity := requestcontext.ThreadIdentityFromContext(ctx)
	if identity.ConversationID == "" {
		identity.ConversationID = proxy.ClientIdentityFrom(ctx).SessionID
	}
	if identity.ConversationID == "" && env != nil {
		identity.ConversationID = env.ClientSessionID()
	}
	installation, _ := ctx.Value(proxy.InstallationIDContextKey{}).(string)
	if installation == "" {
		installation = uuid.Nil.String()
	}
	scope := identity.AutomaticScope(credential, installation)
	copy(scope.Key[:], requestcontext.ServingStateKey(ctx, scope.Key[:]))
	return scope.Key
}
