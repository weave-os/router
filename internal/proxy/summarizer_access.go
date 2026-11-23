package proxy

import (
	"context"
	"net/http"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
)

type summarizerAccess struct {
	s       *Service
	headers http.Header
}

func (a summarizerAccess) SummaryContext(ctx context.Context, provider string) (context.Context, bool) {
	sumCreds := a.s.credentials.SummarizerCredentials(ctx, provider, a.headers)
	nonDepCreds := a.s.credentials.RequestUsesNonDeploymentCreds(ctx, a.headers)
	if sumCreds == nil && nonDepCreds {
		return nil, false
	}
	if sumCreds != nil {
		return context.WithValue(ctx, requestcontext.CredentialsContextKey{}, sumCreds), true
	}
	return credentials.ClearCredentials(ctx), true
}
