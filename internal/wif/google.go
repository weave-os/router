// Package wif provides workload attestation sources that satisfy auth.WIFTokenSource.
// Adapters: talk to a cloud metadata server or read a projected token file.
package wif

import (
	"context"
	"fmt"
	"sync"

	gauth "cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials/idtoken"

	"weave-os/router/internal/auth"
)

// GoogleTokenSource attests the router's own Google service account with a
// Google-signed ID token.
type GoogleTokenSource struct {
	audience string

	mu    sync.Mutex
	creds *gauth.Credentials
}

// NewGoogleTokenSource returns a token source minting ID tokens for audience.
func NewGoogleTokenSource(audience string) *GoogleTokenSource {
	return &GoogleTokenSource{audience: audience}
}

// Attestation returns the Snowflake-shaped GCP credential. The underlying credentials
// cache and refresh the ID token, so the metadata server is hit once per token lifetime.
func (s *GoogleTokenSource) Attestation(ctx context.Context) ([]byte, error) {
	creds, err := s.credentials()
	if err != nil {
		return nil, err
	}
	token, err := creds.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("mint Google ID token for %q: %w", s.audience, err)
	}
	return auth.WIFCredential(auth.WIFProviderGCP, token.Value)
}

// credentials builds the ID-token credentials on first use; deferred so a
// deployment with no WIF keys can still boot without a workload identity.
func (s *GoogleTokenSource) credentials() (*gauth.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creds != nil {
		return s.creds, nil
	}
	creds, err := idtoken.NewCredentials(&idtoken.Options{Audience: s.audience})
	if err != nil {
		return nil, fmt.Errorf("build Google ID-token credentials for %q: %w", s.audience, err)
	}
	s.creds = creds
	return creds, nil
}
