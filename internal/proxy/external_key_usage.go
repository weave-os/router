package proxy

import (
	"context"

	"weave-os/router/internal/credentials"
)

func (s *Service) markServedExternalKeyUsed(ctx context.Context, servedErr error) {
	if servedErr != nil || managedSubscriptionCredentialSource(ctx) != "" {
		return
	}
	s.credentials.MarkServedExternalKeyUsed(ctx, credentials.CredentialsFromContext(ctx))
}
