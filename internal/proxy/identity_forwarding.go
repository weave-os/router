package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"workweave/router/internal/auth"
)

// identityBag is the JSON property bag rendered for auth.IdentityFormatJSON.
// Empty fields are omitted so an endpoint never has to distinguish "absent"
// from "blank".
type identityBag struct {
	UserEmail string `json:"user_email,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ClientApp string `json:"client_app,omitempty"`
}

// ApplyIdentityHeader sets the caller-identity header the request's BYOK
// endpoint asked for. No-op when the key configures none, or when the request
// carries no identity to forward -- an endpoint attributing spend per user is
// better served by an absent header than by an empty one.
//
// Called after the prepared request's own headers are copied so a configured
// name always wins over a client-supplied value of the same name.
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
	// Percent-encoding keeps the value inside the header's token grammar: a
	// display name with a comma or non-ASCII character would otherwise produce
	// a header an upstream may reject or truncate.
	return url.QueryEscape(string(bag))
}
