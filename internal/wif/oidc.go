package wif

import (
	"context"
	"fmt"
	"io"
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

// ReadAnthropicIdentityToken bounds projected-file reads before validating the JWT.
// Snowflake's existing identity-file handling remains independent.
func ReadAnthropicIdentityToken(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open OIDC token file: %w", err)
	}
	defer file.Close()
	const maxIdentityFileBytes = 64 * 1024
	raw, err := io.ReadAll(io.LimitReader(file, maxIdentityFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read OIDC token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if len(raw) > maxIdentityFileBytes || token == "" {
		return "", fmt.Errorf("OIDC token file is empty or exceeds 64 KiB")
	}
	return token, nil
}
