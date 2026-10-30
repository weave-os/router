package wif

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"cloud.google.com/go/auth/credentials/idtoken"
)

// FileIdentityTokenSource rereads a runtime-projected, raw OIDC JWT per exchange.
// The runtime must rotate single-use assertions before the next exchange.
type FileIdentityTokenSource struct{ path string }

func NewFileIdentityTokenSource(path string) *FileIdentityTokenSource {
	return &FileIdentityTokenSource{path: path}
}

func (s *FileIdentityTokenSource) IdentityToken(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("open identity token file: %w", err)
	}
	defer file.Close()
	const maxTokenBytes = 64 * 1024
	raw, err := io.ReadAll(io.LimitReader(file, maxTokenBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read identity token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || len(raw) > maxTokenBytes {
		return nil, fmt.Errorf("identity token file is empty or oversized")
	}
	return []byte(token), nil
}

// GoogleIdentityTokenSource fetches a raw Google ID token for each exchange.
// It deliberately does not share Snowflake's cached ID-token credentials.
type GoogleIdentityTokenSource struct{ audience string }

func NewGoogleIdentityTokenSource(audience string) *GoogleIdentityTokenSource {
	return &GoogleIdentityTokenSource{audience: audience}
}

func (s *GoogleIdentityTokenSource) IdentityToken(ctx context.Context) ([]byte, error) {
	creds, err := idtoken.NewCredentials(&idtoken.Options{Audience: s.audience})
	if err != nil {
		return nil, fmt.Errorf("build Google identity credentials: %w", err)
	}
	token, err := creds.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("mint Google identity token: %w", err)
	}
	return []byte(token.Value), nil
}
