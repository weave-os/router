package wif

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"weave-os/router/internal/auth"
)

type FileTokenSource struct {
	path string
}

func NewFileTokenSource(path string) *FileTokenSource {
	return &FileTokenSource{path: path}
}

func (s *FileTokenSource) Attestation(ctx context.Context) ([]byte, error) {
	token, err := ReadIdentityToken(ctx, s.path)
	if err != nil {
		return nil, err
	}
	return auth.WIFCredential(auth.WIFProviderOIDC, token)
}

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
