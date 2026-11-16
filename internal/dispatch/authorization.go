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

var ErrAuthorizationDenied = errors.New("inference funding authorization denied")

type Authorization func(context.Context, inference.Target) error

type authorizationContextKey struct{}

func WithAuthorization(ctx context.Context, authorize Authorization) context.Context {
	return context.WithValue(ctx, authorizationContextKey{}, authorize)
}

func AuthorizationFromContext(ctx context.Context) Authorization {
	authorize, _ := ctx.Value(authorizationContextKey{}).(Authorization)
	return authorize
}

type authorizationGuard struct {
	providers.Client
	target    inference.Target
	authorize Authorization
}

func (g authorizationGuard) Unwrap() providers.Client { return g.Client }

func (g authorizationGuard) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	if err := g.authorize(ctx, g.target); err != nil {
		return fmt.Errorf("%w: %w", ErrAuthorizationDenied, err)
	}
	return g.Client.Proxy(ctx, decision, prep, w, r)
}
