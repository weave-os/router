package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"weave-os/router/internal/auth"
)

// identityBag is the JSON property bag rendered for auth.IdentityFormatJSON; empty fields omitted.
type identityBag struct {
	UserEmail string `json:"user_email,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ClientApp string `json:"client_app,omitempty"`
}

// ApplyIdentityHeader sets the caller-identity header the BYOK endpoint configured;
// must be called after prep.Headers are copied so it wins over a client-supplied value.
func ApplyIdentityHeader(ctx context.Context, upstream *http.Request) {
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.IdentityHeader == "" {
		return
	}
	value := identityHeaderValue(creds.IdentityHeaderFormat, ClientIdentityFrom(ctx))
	if value == "" {
		return
	}
	upstream.Header.Set(creds.IdentityHeader, value)
}

// identityHeaderValue renders identity in the endpoint's configured format,
// returning "" when there is nothing worth sending.
func identityHeaderValue(format string, identity ClientIdentity) string {
	if identity.Email == "" {
		return ""
	}
	if format == auth.IdentityFormatEmail {
		return identity.Email
	}
	bag, err := json.Marshal(identityBag{
		UserEmail: identity.Email,
		UserName:  identity.DisplayName,
		SessionID: identity.SessionID,
		ClientApp: identity.ClientApp,
	})
	if err != nil {
		return ""
	}
	// Percent-encode with %20, not "+": QueryEscape uses form-encoding, which decodeURIComponent reads literally.
	return strings.ReplaceAll(url.QueryEscape(string(bag)), "+", "%20")
}
