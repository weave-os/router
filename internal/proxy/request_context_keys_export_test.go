package proxy

import "weave-os/router/internal/requestcontext"

type (
	APIKeyIDContextKey           = requestcontext.APIKeyIDContextKey
	ExternalAPIKeysContextKey    = requestcontext.ExternalAPIKeysContextKey
	InstallationIDContextKey     = requestcontext.InstallationIDContextKey
	OpenAIAccountIDContextKey    = requestcontext.OpenAIAccountIDContextKey
	OpenAISubscriptionContextKey = requestcontext.OpenAISubscriptionContextKey
)
