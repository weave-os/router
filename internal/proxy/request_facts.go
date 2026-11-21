package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
)

func requestFactsFrom(ctx context.Context, headers http.Header) requestcontext.RequestFacts {
	externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	anthropicSubscription, _ := ctx.Value(credentials.AnthropicSubscriptionContextKey{}).(string)
	openAISubscription, _ := ctx.Value(credentials.OpenAISubscriptionContextKey{}).(string)
	return requestcontext.RequestFacts{
		RequestID:             requestIDFor(ctx),
		APIKeyID:              apiKeyIDFromContext(ctx),
		ExternalID:            externalID,
		InstallationID:        installationIDFromContext(ctx),
		Client:                ClientIdentityFrom(ctx),
		Headers:               headers,
		ExternalKeys:          credentials.ExternalKeysFromContext(ctx),
		AnthropicSubscription: anthropicSubscription,
		OpenAISubscription:    openAISubscription,
		OpenAIAccountID:       openaiAccountIDFromContext(ctx),
		ByokFeeUnpayable:      billing.ByokFeeUnpayable(ctx),
	}
}
