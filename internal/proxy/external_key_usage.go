package proxy

import "context"

// ExternalKeyUsageRecorder stamps a stored customer provider key as used.
type ExternalKeyUsageRecorder interface {
	MarkExternalAPIKeyUsed(ctx context.Context, id string)
}

// WithExternalKeyUsageRecorder wires the recorder that keeps a provider key's
// "Last used" current once it serves a request.
func (s *Service) WithExternalKeyUsageRecorder(recorder ExternalKeyUsageRecorder) *Service {
	s.externalKeyUsage = recorder
	return s
}

// markServedExternalKeyUsed records use of the stored customer key that served
// this request. Failed requests and every other credential source are skipped.
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
