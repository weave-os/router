package requestcontext

import "context"

// InternalTestIdentity carries signed subject and fresh launch-session attribution.
type InternalTestIdentity struct{ SubjectID, SessionID string }
type internalTestContextKey struct{}

// WithInternalTestIdentity is populated only after gateway assertion verification.
func WithInternalTestIdentity(ctx context.Context, identity InternalTestIdentity) context.Context {
	return context.WithValue(ctx, internalTestContextKey{}, identity)
}

// InternalTestIdentityFrom returns only server-populated test attribution.
func InternalTestIdentityFrom(ctx context.Context) (InternalTestIdentity, bool) {
	identity, ok := ctx.Value(internalTestContextKey{}).(InternalTestIdentity)
	return identity, ok
}
