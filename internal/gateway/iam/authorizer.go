// Package iam supplies Cloud Run identity tokens independently of client authorization.
package iam

import (
	"context"

	"google.golang.org/api/idtoken"
)

// Authorizer relies on the gateway service identity's target-specific run.invoker grants.
type Authorizer struct{}

// IdentityToken uses the service audience explicitly recorded in the validated revision binding.
func (Authorizer) IdentityToken(ctx context.Context, audience string) (string, error) {
	source, err := idtoken.NewTokenSource(ctx, audience)
	if err != nil {
		return "", err
	}
	token, err := source.Token()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}
