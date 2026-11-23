package credentials

import (
	"context"
	"net/http"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

func RequestPresentsCoveringSubscription(ctx context.Context, headers http.Header, routePath string) bool {
	if SubscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	codex, anthropic := PresentSubscriptionTokens(ctx, headers)
	switch routePath {
	case requestcontext.RoutePathMessages, requestcontext.RoutePathPiHandoff:
		return anthropic != "" || ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderClaude)
	case requestcontext.RoutePathChatCompletions, requestcontext.RoutePathResponses:
		return codex != "" || ManagedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex)
	default:
		return false
	}
}

func RequestHasCustomerFunding(ctx context.Context, headers http.Header, routePath string, byokFeeOwed bool) bool {
	if RequestPresentsCoveringSubscription(ctx, headers, routePath) {
		return true
	}
	if byokFeeOwed {
		return false
	}
	for provider := range BuildCredentialsMap(ExternalKeysFromContext(ctx)) {
		if UsableBYOK(ctx, provider) != nil {
			return true
		}
	}
	return false
}
