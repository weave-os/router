package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"workweave/router/internal/auth"
)

// identityBag is the JSON property bag rendered for auth.IdentityFormatJSON; empty fields omitted.
type identityBag struct {
	UserEmail string `json:"user_email,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ClientApp string `json:"client_app,omitempty"`
}

// ApplyIdentityHeader sets the caller-identity header the BYOK endpoint configured.
// No-op when none is configured, or when the request carries no identity.
// Must be called after prep.Headers are copied so it wins over a client-supplied value.
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
	// Percent-encode so commas and non-ASCII in display names don't break the header grammar.
	// QueryEscape's "+" for space is form encoding: a decodeURIComponent on the
	// far side would read it literally and corrupt the name.
	return strings.ReplaceAll(url.QueryEscape(string(bag)), "+", "%20")
}
