package requestcontext

import "context"

type InternalTestIdentity struct{ SubjectID, SessionID string }
type internalTestContextKey struct{}

func WithInternalTestIdentity(ctx context.Context, identity InternalTestIdentity) context.Context {
	return context.WithValue(ctx, internalTestContextKey{}, identity)
}

func InternalTestIdentityFrom(ctx context.Context) (InternalTestIdentity, bool) {
	identity, ok := ctx.Value(internalTestContextKey{}).(InternalTestIdentity)
	return identity, ok
}
