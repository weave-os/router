package proxy

import (
	"context"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
)

func subscriptionFundingFailure(err error) bool {
	return credentials.IsSubscriptionPoolError(err) || credentials.SubscriptionLaneRefused(err) ||
		codexSubscriptionModelRejected(err) || anthropicSubscriptionModelRejected(err) ||
		codexOAuthCredentialRejected(err) || anthropicOAuthCredentialRejected(err) || credentials.AnthropicSubscriptionThirdPartyRefused(err)
}

func strikeModelUnlessFundingFailure(ctx context.Context, model string, err error) {
	if subscriptionFundingFailure(err) {
		return
	}
	requestcontext.RecordTurnFailure(ctx, model)
}
