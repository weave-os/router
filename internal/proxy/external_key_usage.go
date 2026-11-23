package proxy

import (
	"context"

	"weave-os/router/internal/credentials"
)

func (s *Service) WithExternalKeyUsageRecorder(recorder credentials.ExternalKeyUsageRecorder) *Service {
	s.credentials.ExternalKeyUsage = recorder
	return s
}

func (s *Service) markServedExternalKeyUsed(ctx context.Context, servedErr error) {
	if servedErr != nil || managedSubscriptionCredentialSource(ctx) != "" {
		return
	}
	s.credentials.MarkServedExternalKeyUsed(ctx, credentials.CredentialsFromContext(ctx))
}
