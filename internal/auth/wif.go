package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Snowflake workload identity federation constants. The token-type header
// tells the upstream to read the bearer as a federated identity, not a Snowflake-issued token.
const (
	WIFTokenTypeHeader = "X-Snowflake-Authorization-Token-Type"
	WIFTokenTypeValue  = "WORKLOAD_IDENTITY_FEDERATION"
	// WIFAudience is the audience Snowflake requires in a workload's ID token.
	WIFAudience = "snowflakecomputing.com"
)

// WIF attestation providers Snowflake accepts as the credential's first segment.
const (
	WIFProviderGCP  = "GCP"
	WIFProviderOIDC = "OIDC"
)

// ErrWIFUnavailable is returned when a key asks for workload identity but the
// deployment has no attestation source wired.
var ErrWIFUnavailable = errors.New("auth: workload identity federation is not configured")

// WIFTokenSource produces the router's own workload attestation. Implementations
// talk to a metadata server or filesystem, so every call must honour ctx.
type WIFTokenSource interface {
	// Attestation returns the full credential to send as the bearer value,
	// already in Snowflake's WIF.<provider>.<token> form.
	Attestation(ctx context.Context) ([]byte, error)
}

// WIFCredential formats a raw attestation as the bearer value Snowflake expects.
func WIFCredential(provider, token string) ([]byte, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: %s attestation is empty", ErrWIFUnavailable, provider)
	}
	return []byte("WIF." + provider + "." + token), nil
}
