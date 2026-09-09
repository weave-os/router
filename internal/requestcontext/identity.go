package requestcontext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"weave-os/router/internal/auth"
)

// ClientIdentity holds per-request user identification signals, persisted to
// router.model_router_users (Email, DisplayName).
type ClientIdentity struct {
	DeviceID    string
	AccountID   string
	SessionID   string
	Email       string
	DisplayName string
	UserAgent   string
	ClientApp   string
	// RolloutID is the x-weave-rollout-id eval/training-harness correlation
	// id; joins a sandbox rollout's graded reward to its routing decisions.
	RolloutID string
}

// ClientIdentityContextKey is the request-context key for client identity.
type ClientIdentityContextKey struct{}

// ClientIdentityFrom reads the ClientIdentity stashed on ctx.
func ClientIdentityFrom(ctx context.Context) ClientIdentity {
	id, _ := ctx.Value(ClientIdentityContextKey{}).(ClientIdentity)
	return id
}

// WithClientIdentity stashes id on ctx.
func WithClientIdentity(ctx context.Context, id ClientIdentity) context.Context {
	return context.WithValue(ctx, ClientIdentityContextKey{}, id)
}

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
	if creds == nil || !safeCredentialHeaderDestination(creds, creds.IdentityHeader) {
		return
	}
	value := IdentityHeaderValue(creds.IdentityHeaderFormat, ClientIdentityFrom(ctx))
	if value == "" {
		return
	}
	upstream.Header.Set(creds.IdentityHeader, value)
}

// IdentityHeaderValue renders identity in the endpoint's configured format,
// returning "" when there is nothing worth sending.
func IdentityHeaderValue(format string, identity ClientIdentity) string {
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
