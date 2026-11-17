package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/requestcontext"
)

func requestFactsFrom(ctx context.Context, headers http.Header) requestcontext.RequestFacts {
	externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	anthropicSubscription, _ := ctx.Value(AnthropicSubscriptionContextKey{}).(string)
	openAISubscription, _ := ctx.Value(OpenAISubscriptionContextKey{}).(string)
	return requestcontext.RequestFacts{
		RequestID:             requestIDFor(ctx),
		APIKeyID:              apiKeyIDFromContext(ctx),
		ExternalID:            externalID,
		InstallationID:        installationIDFromContext(ctx),
		Client:                ClientIdentityFrom(ctx),
		Headers:               headers,
		ExternalKeys:          externalKeysFromContext(ctx),
		AnthropicSubscription: anthropicSubscription,
		OpenAISubscription:    openAISubscription,
		OpenAIAccountID:       openaiAccountIDFromContext(ctx),
	}
}
