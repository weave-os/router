package proxy

import (
	"context"
	"net/http"
)

type summarizerAccess struct {
	s       *Service
	headers http.Header
}

func (a summarizerAccess) SummaryContext(ctx context.Context, provider string) (context.Context, bool) {
	sumCreds := resolveSummarizerCreds(ctx, provider, a.headers)
	nonDepCreds := a.s.requestUsesNonDeploymentCreds(ctx, a.headers)
	if sumCreds == nil && nonDepCreds {
		return nil, false
	}
	if sumCreds != nil {
		return context.WithValue(ctx, CredentialsContextKey{}, sumCreds), true
	}
	return clearCredentials(ctx), true
}
