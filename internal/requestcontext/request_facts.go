package requestcontext

import (
	"net/http"

	"weave-os/router/internal/auth"

	"github.com/google/uuid"
)

type RequestFacts struct {
	RequestID             string
	APIKeyID              string
	ExternalID            string
	InstallationID        uuid.UUID
	Client                ClientIdentity
	Headers               http.Header
	ExternalKeys          []*auth.ExternalAPIKey
	AnthropicSubscription string
	OpenAISubscription    string
	OpenAIAccountID       string
}

func (f RequestFacts) RouterKeyed() bool {
	return f.InstallationID != uuid.Nil
}
