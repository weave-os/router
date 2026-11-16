package proxy

import "context"

type ExternalKeyUsageRecorder interface {
	MarkExternalAPIKeyUsed(ctx context.Context, id string)
}

func (s *Service) WithExternalKeyUsageRecorder(recorder ExternalKeyUsageRecorder) *Service {
	s.externalKeyUsage = recorder
	return s
}

func (s *Service) markServedExternalKeyUsed(ctx context.Context, servedErr error) {
	if s.externalKeyUsage == nil || servedErr != nil || managedSubscriptionCredentialSource(ctx) != "" {
		return
	}
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.Source != credSourceBYOK || creds.ExternalKeyID == "" {
		return
	}
	s.externalKeyUsage.MarkExternalAPIKeyUsed(ctx, creds.ExternalKeyID)
}
