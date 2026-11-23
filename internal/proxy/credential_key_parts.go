package proxy

import (
	"context"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
)

func (s *Service) credentialKeyParts(ctx context.Context) (prefix, suffix, source string) {
	if managedSource := managedSubscriptionCredentialSource(ctx); managedSource != "" {
		if managedSource == requestcontext.SourceSubscription && s.subscriptionOverageInUse(ctx) {
			return "", "", requestcontext.SourceSubscriptionOverage
		}
		return "", "", managedSource
	}
	creds := credentials.CredentialsFromContext(ctx)
	if creds == nil || len(creds.APIKey) == 0 {
		return "", "", ""
	}
	prefix, suffix = credentials.KeyParts(creds.APIKey)
	return prefix, suffix, s.telemetryCredentialSource(ctx, creds.Source)
}

func (s *Service) telemetryCredentialSource(ctx context.Context, source string) string {
	if source == requestcontext.SourceSubscription && s.subscriptionOverageInUse(ctx) {
		return requestcontext.SourceSubscriptionOverage
	}
	return source
}
