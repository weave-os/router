package proxy

import (
	"context"
	"errors"
	"fmt"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

var ErrModelListingUnsupported = errors.New("provider does not support model listing")

func (s *Service) ListUpstreamModels(ctx context.Context, provider string, creds *requestcontext.Credentials) ([]string, error) {
	client, err := s.engine.Clients().Client(provider)
	if err != nil {
		return nil, err
	}
	lister, ok := client.(providers.ModelLister)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrModelListingUnsupported, provider)
	}
	if creds != nil {
		ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, creds)
	}
	return lister.ListModels(ctx)
}
