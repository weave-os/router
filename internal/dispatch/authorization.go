package dispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

// ErrAuthorizationDenied marks a pre-I/O funding denial, never a retryable fault.
var ErrAuthorizationDenied = errors.New("inference funding authorization denied")

// Authorization checks final attempt credentials against request admission facts.
type Authorization func(context.Context, inference.Target) error

type authorizationContextKey struct{}

// WithAuthorization carries funding authorization across credential re-resolution
// and auxiliary operations. Run captures it before attempt contexts are prepared.
func WithAuthorization(ctx context.Context, authorize Authorization) context.Context {
	return context.WithValue(ctx, authorizationContextKey{}, authorize)
}

// AuthorizationFromContext returns the funding check, or nil for an unguarded
// context. Run skips funding authorization when no check is present.
func AuthorizationFromContext(ctx context.Context) Authorization {
	authorize, _ := ctx.Value(authorizationContextKey{}).(Authorization)
	return authorize
}

type authorizationGuard struct {
	providers.Client
	target    inference.Target
	authorize Authorization
}

// Unwrap exposes the guarded client, e.g. to read its deployment endpoint.
func (g authorizationGuard) Unwrap() providers.Client { return g.Client }

func (g authorizationGuard) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	if err := g.authorize(ctx, g.target); err != nil {
		return fmt.Errorf("%w: %w", ErrAuthorizationDenied, err)
	}
	return g.Client.Proxy(ctx, decision, prep, w, r)
}
