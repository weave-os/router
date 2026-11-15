package requestcontext

import (
	"net/http"

	"weave-os/router/internal/auth"

	"github.com/google/uuid"
)

// RequestFacts are the caller facts the request boundary resolved before the
// orchestrator runs: who is calling, through which key and installation, with
// which client hints and caller-held credentials. They are read once, so
// downstream contexts take them as an explicit value instead of re-reading
// request-context keys.
type RequestFacts struct {
	// RequestID correlates the request's logs and telemetry rows.
	RequestID string
	// APIKeyID is the authenticated router key, empty off the router-key path.
	APIKeyID string
	// ExternalID is the installation's external organization id.
	ExternalID string
	// InstallationID is uuid.Nil when the request is not router-keyed.
	InstallationID uuid.UUID
	Client         ClientIdentity
	// Headers are the inbound headers, which can carry a client credential.
	Headers http.Header
	// ExternalKeys are the installation's connected provider keys.
	ExternalKeys []*auth.ExternalAPIKey
	// AnthropicSubscription, OpenAISubscription and OpenAIAccountID are the
	// subscription credentials an internal caller already authenticated, as
	// stored; funding-priority rules have not been applied to them.
	AnthropicSubscription string
	OpenAISubscription    string
	OpenAIAccountID       string
}

// RouterKeyed reports whether the request authenticated with a router key
// bound to an installation, which rules out forwarding a client API key.
func (f RequestFacts) RouterKeyed() bool {
	return f.InstallationID != uuid.Nil
}
