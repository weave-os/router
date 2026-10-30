package wif

import (
	"context"
	"fmt"
	"os"
	"strings"

	"weave-os/router/internal/auth"
)

// FileTokenSource reads a projected OIDC token from disk on every call;
// the projecting runtime rewrites the file in place before expiry.
type FileTokenSource struct {
	path string
}

// NewFileTokenSource returns a token source reading the OIDC token at path.
func NewFileTokenSource(path string) *FileTokenSource {
	return &FileTokenSource{path: path}
}

// Attestation returns the Snowflake-shaped OIDC credential.
func (s *FileTokenSource) Attestation(ctx context.Context) ([]byte, error) {
	token, err := ReadIdentityToken(ctx, s.path)
	if err != nil {
		return nil, err
	}
	return auth.WIFCredential(auth.WIFProviderOIDC, token)
}

// ReadIdentityToken reopens the projected file so every exchange sees rotation.
func ReadIdentityToken(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read OIDC token file: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}
