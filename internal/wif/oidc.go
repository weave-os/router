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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read OIDC token file: %w", err)
	}
	return auth.WIFCredential(auth.WIFProviderOIDC, strings.TrimSpace(string(raw)))
}
