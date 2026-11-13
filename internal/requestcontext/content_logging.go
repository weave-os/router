package requestcontext

import "context"

type contentLoggingKey struct{}

// WithContentLogging records whether request/response content may appear in
// log fields. Provider adapters and httputil helpers read it so a
// zero-retention installation's bodies never reach stdout logs even on code
// paths that cannot see the proxy capture ceiling. Absence means allowed —
// the flag is only cleared by the serving path once it knows the effective
// capture mode.
func WithContentLogging(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, contentLoggingKey{}, allowed)
}

// ContentLoggingAllowed reports whether content-bearing log fields may be
// emitted for this request. Defaults to true when unset so non-proxy callers
// (admin, passthrough, tests) keep their existing diagnostics.
func ContentLoggingAllowed(ctx context.Context) bool {
	allowed, ok := ctx.Value(contentLoggingKey{}).(bool)
	return !ok || allowed
}
