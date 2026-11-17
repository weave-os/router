package wif

import (
	"context"
	"fmt"
	"sync"

	gauth "cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials/idtoken"

	"weave-os/router/internal/auth"
)

type GoogleTokenSource struct {
	audience string

	mu    sync.Mutex
	creds *gauth.Credentials
}

func NewGoogleTokenSource(audience string) *GoogleTokenSource {
	return &GoogleTokenSource{audience: audience}
}

func (s *GoogleTokenSource) Attestation(ctx context.Context) ([]byte, error) {
	creds, err := s.credentialsOnFirstUse()
	if err != nil {
		return nil, err
	}
	token, err := creds.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("mint Google ID token for %q: %w", s.audience, err)
	}
	return auth.WIFCredential(auth.WIFProviderGCP, token.Value)
}

func (s *GoogleTokenSource) credentialsOnFirstUse() (*gauth.Credentials, error) {
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

func GoogleIdentityToken(ctx context.Context, audience string) (string, error) {
	creds, err := idtoken.NewCredentials(&idtoken.Options{Audience: audience})
	if err != nil {
		return "", fmt.Errorf("build federation identity credentials: %w", err)
	}
	token, err := creds.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("obtain federation identity token: %w", err)
	}
	return token.Value, nil
}
