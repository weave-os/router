package credentials

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
)

func RequestFactsFrom(ctx context.Context, headers http.Header) requestcontext.RequestFacts {
	externalID, _ := ctx.Value(requestcontext.ExternalIDContextKey{}).(string)
	anthropicSubscription, _ := ctx.Value(AnthropicSubscriptionContextKey{}).(string)
	openAISubscription, _ := ctx.Value(OpenAISubscriptionContextKey{}).(string)
	return requestcontext.RequestFacts{
		RequestID:             requestIDFor(ctx),
		APIKeyID:              requestcontext.APIKeyIDFromContext(ctx),
		ExternalID:            externalID,
		InstallationID:        requestcontext.InstallationIDFromContext(ctx),
		Client:                requestcontext.ClientIdentityFrom(ctx),
		Headers:               headers,
		ExternalKeys:          ExternalKeysFromContext(ctx),
		AnthropicSubscription: anthropicSubscription,
		OpenAISubscription:    openAISubscription,
		OpenAIAccountID:       requestcontext.OpenAIAccountIDFromContext(ctx),
		ByokFeeUnpayable:      billing.ByokFeeUnpayable(ctx),
	}
}

func requestIDFor(ctx context.Context) string {
	if id := observability.RequestIDFromContext(ctx); id != "" {
		return id
	}
	return uuid.New().String()
}
