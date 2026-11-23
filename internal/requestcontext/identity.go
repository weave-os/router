package requestcontext

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"weave-os/router/internal/auth"
)

type ClientIdentity struct {
	DeviceID         string
	AccountID        string
	SessionID        string
	Email            string
	DisplayName      string
	UserAgent        string
	ClientApp        string
	Eval             bool
	RolloutID        string
	OpenCodeSubagent bool
}

const OpenCodeSessionHeader = "X-OpenCode-Session"

const OpenCodeParentSessionHeader = "X-Parent-Session-Id"

const EvalClientAppPrefix = "weave-eval-"

func (id ClientIdentity) TelemetryClientApp() string {
	if id.Eval && id.ClientApp != "" {
		return EvalClientAppPrefix + id.ClientApp
	}
	return id.ClientApp
}

type ClientIdentityContextKey struct{}

func ClientIdentityFrom(ctx context.Context) ClientIdentity {
	id, _ := ctx.Value(ClientIdentityContextKey{}).(ClientIdentity)
	return id
}

func WithClientIdentity(ctx context.Context, id ClientIdentity) context.Context {
	if test, ok := InternalTestIdentityFrom(ctx); ok {
		id.Email = ""
		id.AccountID = test.SubjectID
		id.DeviceID = ""
		id.DisplayName = ""
		id.SessionID = test.SessionID
		id.Eval = false
		id.RolloutID = ""
	}
	if upstreamGateway, ok := UpstreamGatewayFrom(ctx); ok {
		id.Email = upstreamGateway.Email
		id.AccountID = ""
		id.DeviceID = ""
		id.DisplayName = upstreamGateway.DisplayName
		id.SessionID = upstreamGateway.SessionID
	}
	return context.WithValue(ctx, ClientIdentityContextKey{}, id)
}

type identityBag struct {
	UserEmail string `json:"user_email,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ClientApp string `json:"client_app,omitempty"`
}

func ApplyIdentityHeader(ctx context.Context, upstream *http.Request) {
	creds := CredentialsFromContext(ctx)
	if creds == nil || !safeCredentialHeaderDestination(creds, creds.IdentityHeader) {
		return
	}
	if _, gateway := UpstreamGatewayFrom(ctx); gateway {
		return
	}
	value := IdentityHeaderValue(creds.IdentityHeaderFormat, ClientIdentityFrom(ctx))
	if value == "" {
		return
	}
	upstream.Header.Set(creds.IdentityHeader, value)
}

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
	return strings.ReplaceAll(url.QueryEscape(string(bag)), "+", "%20")
}
