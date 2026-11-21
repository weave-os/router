package credentials

import (
	"context"
	"net/http"

	"weave-os/router/internal/requestcontext"
)

func (b *Brokerage) SummarizerCredentials(ctx context.Context, provider string, headers http.Header) *requestcontext.Credentials {
	if provider == "" {
		return nil
	}
	if creds, ok := BuildCredentialsMap(ExternalKeysFromContext(ctx))[provider]; ok {
		return creds
	}
	creds := requestcontext.ExtractClientCredentials(provider, headers)
	if creds != nil && creds.OAuth {
		return nil
	}
	return creds
}

func (b *Brokerage) RequestUsesNonDeploymentCreds(ctx context.Context, headers http.Header) bool {
	return b.UsesNonDeploymentCreds(ExternalKeysFromContext(ctx), headers)
}
