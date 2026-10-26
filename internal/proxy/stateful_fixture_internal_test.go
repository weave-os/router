package proxy

import (
	"context"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

func statefulTestContext() context.Context {
	ctx := requestcontext.WithClientIdentity(context.Background(), ClientIdentity{SessionID: "synthetic-test-conversation"})
	ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "key-1")
	return requestcontext.WithThreadIdentity(ctx, requestcontext.ThreadIdentity{ConversationID: "synthetic-test-conversation", ThreadID: "synthetic-test-thread", Source: requestcontext.ThreadSourceExplicit, Status: requestcontext.ThreadIdentityAvailable})
}

func withTestThread(ctx context.Context, threadID string) context.Context {
	identity := requestcontext.ThreadIdentityFromContext(ctx)
	identity.ThreadID = threadID
	return requestcontext.WithThreadIdentity(ctx, identity)
}

func DeriveSessionKey(env *translate.RequestEnvelope, credential string) [16]byte {
	return deriveSessionKeyForRequest(statefulTestContext(), env, credential)
}
