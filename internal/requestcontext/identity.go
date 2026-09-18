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
	// ClientApp is the canonical harness (codex, claude-code, ...). Every
	// harness-keyed behaviour reads this, so an eval run is served exactly
	// like the production client it imitates.
	ClientApp string
	// Eval marks traffic from a Weave eval harness (X-App carried the
	// EvalClientAppPrefix). Telemetry reads TelemetryClientApp so dashboards
	// and alerts can exclude the run; nothing on the request path reads it.
	Eval bool
	// RolloutID is the x-weave-rollout-id eval/training-harness correlation
	// id; joins a sandbox rollout's graded reward to its routing decisions.
	RolloutID string
	// OpenCodeAgent is a known OpenCode chat.headers agent, or empty.
	// Metadata only: never used for auth, billing, or provider eligibility.
	OpenCodeAgent OpenCodeAgent
}

// OpenCodeAgent is the typed OpenCode chat.headers agent vocabulary.
type OpenCodeAgent string

const (
	OpenCodeAgentBuild      OpenCodeAgent = "build"
	OpenCodeAgentTitle      OpenCodeAgent = "title"
	OpenCodeAgentExplore    OpenCodeAgent = "explore"
	OpenCodeAgentCompaction OpenCodeAgent = "compaction"
)

// OpenCodeAgentHeader is the production OpenCode lifecycle header.
const OpenCodeAgentHeader = "X-Weave-OpenCode-Agent"

// ParseOpenCodeAgent accepts only the known OpenCode agent values.
func ParseOpenCodeAgent(raw string) OpenCodeAgent {
	agent := OpenCodeAgent(strings.TrimSpace(raw))
	switch agent {
	case OpenCodeAgentBuild, OpenCodeAgentTitle, OpenCodeAgentExplore, OpenCodeAgentCompaction:
		return agent
	default:
		return ""
	}
}

// EvalClientAppPrefix is the X-App prefix an eval harness puts in front of
// the client it imitates ("weave-eval-codex").
const EvalClientAppPrefix = "weave-eval-"

// TelemetryClientApp is the client_app value recorded on spans, telemetry
// rows and completion logs: the canonical app, prefixed for eval traffic.
func (id ClientIdentity) TelemetryClientApp() string {
	if id.Eval && id.ClientApp != "" {
		return EvalClientAppPrefix + id.ClientApp
	}
	return id.ClientApp
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
