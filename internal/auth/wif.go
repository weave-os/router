package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	WIFTokenTypeHeader = "X-Snowflake-Authorization-Token-Type"
	WIFTokenTypeValue  = "WORKLOAD_IDENTITY_FEDERATION"
	WIFAudience        = "snowflakecomputing.com"
)

const (
	WIFProviderGCP  = "GCP"
	WIFProviderOIDC = "OIDC"
)

var ErrWIFUnavailable = errors.New("auth: workload identity federation is not configured")

type WIFTokenSource interface {
	Attestation(ctx context.Context) ([]byte, error)
}

func WIFCredential(provider, token string) ([]byte, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: %s attestation is empty", ErrWIFUnavailable, provider)
	}
	return []byte("WIF." + provider + "." + token), nil
}
