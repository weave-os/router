package proxy

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/providers"
)

// ErrModelListingUnsupported is returned when the provider's adapter has no
// model-listing surface; callers keep the manual alias-entry path.
var ErrModelListingUnsupported = errors.New("provider does not support model listing")

// ListUpstreamModels queries a provider endpoint for the model IDs it publishes,
// authenticating with creds exactly as an inference call would (nil creds → deployment-level key).
func (s *Service) ListUpstreamModels(ctx context.Context, provider string, creds *Credentials) ([]string, error) {
	client, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotConfigured, provider)
	}
	lister, ok := client.(providers.ModelLister)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrModelListingUnsupported, provider)
	}
	if creds != nil {
		ctx = context.WithValue(ctx, CredentialsContextKey{}, creds)
	}
	return lister.ListModels(ctx)
}
