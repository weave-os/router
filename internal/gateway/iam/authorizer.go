// Package iam supplies Cloud Run identity tokens independently of client authorization.
package iam

import (
	"context"
	"golang.org/x/sync/singleflight"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

// Authorizer relies on the gateway service identity's target-specific run.invoker grants.
type Authorizer struct {
	ctx       context.Context
	refresh   singleflight.Group
	mu        sync.Mutex
	sources   map[string]oauth2.TokenSource
	newSource func(context.Context, string) (oauth2.TokenSource, error)
}

// NewAuthorizer retains audience-specific token sources for the process lifetime.
func NewAuthorizer(ctx context.Context) *Authorizer {
	// SIGTERM stops admission before in-flight requests finish forwarding.
	ctx = context.WithValue(context.WithoutCancel(ctx), oauth2.HTTPClient, &http.Client{Timeout: 10 * time.Second})
	return &Authorizer{ctx: ctx, sources: make(map[string]oauth2.TokenSource), newSource: func(ctx context.Context, audience string) (oauth2.TokenSource, error) {
		return idtoken.NewTokenSource(ctx, audience)
	}}
}

// IdentityToken uses the service audience explicitly recorded in the validated revision binding.
func (a *Authorizer) IdentityToken(ctx context.Context, audience string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pending := a.refresh.DoChan(audience, func() (any, error) {
		a.mu.Lock()
		source := a.sources[audience]
		if source == nil {
			created, err := a.newSource(a.ctx, audience)
			if err != nil {
				a.mu.Unlock()
				return "", err
			}
			source = oauth2.ReuseTokenSource(nil, created)
			a.sources[audience] = source
		}
		a.mu.Unlock()
		token, err := source.Token()
		if err != nil {
			return "", err
		}
		return token.AccessToken, nil
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-pending:
		if result.Err != nil {
			return "", result.Err
		}
		return result.Val.(string), nil
	}
}
