package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
)

// ApplyWIFTokenType marks the bearer as a workload attestation, not an upstream-issued token.
// Must be called after prep.Headers are copied so a client-supplied value can't suppress it.
func ApplyWIFTokenType(ctx context.Context, upstream *http.Request) {
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.AuthType != auth.AuthTypeWIF {
		return
	}
	upstream.Header.Set(auth.WIFTokenTypeHeader, auth.WIFTokenTypeValue)
}
