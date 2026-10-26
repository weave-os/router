package proxy_test

import (
	"context"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
)

func statefulTestContext() context.Context {
	ctx := requestcontext.WithClientIdentity(context.Background(), proxy.ClientIdentity{SessionID: "synthetic-test-conversation"})
	ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "key-1")
	return requestcontext.WithThreadIdentity(ctx, requestcontext.ThreadIdentity{ConversationID: "synthetic-test-conversation", ThreadID: "synthetic-test-thread", Source: requestcontext.ThreadSourceExplicit, Status: requestcontext.ThreadIdentityAvailable})
}
