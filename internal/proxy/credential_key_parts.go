package proxy

import (
	"context"

	"weave-os/router/internal/credentials"
)

// credentialKeyParts returns the safe display parts and source of the turn's
// resolved upstream credential. Empty values mean the turn ran on the deployment
// key or no credential was resolved.
func (s *Service) credentialKeyParts(ctx context.Context) (prefix, suffix, source string) {
	if managedSource := managedSubscriptionCredentialSource(ctx); managedSource != "" {
		if managedSource == credSourceSubscription && s.subscriptionOverageInUse(ctx) {
			return "", "", credSourceSubscriptionOverage
		}
		return "", "", managedSource
	}
	creds := CredentialsFromContext(ctx)
	if creds == nil || len(creds.APIKey) == 0 {
		return "", "", ""
	}
	prefix, suffix = credentials.KeyParts(creds.APIKey)
	return prefix, suffix, s.telemetryCredentialSource(ctx, creds.Source)
}

func (s *Service) telemetryCredentialSource(ctx context.Context, source string) string {
	if source == credSourceSubscription && s.subscriptionOverageInUse(ctx) {
		return credSourceSubscriptionOverage
	}
	return source
}
