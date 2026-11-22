package proxy

import (
	"context"
	"errors"
	"weave-os/router/internal/credentials"
)

func isSubscriptionPoolError(err error) bool {
	return errors.Is(err, credentials.ErrSubscriptionPoolExhausted) || errors.Is(err, credentials.ErrSubscriptionPoolUnavailable) || errors.Is(err, ErrClaudeEnterpriseSubscriptionUnavailable)
}

func subscriptionFundingFailure(err error) bool {
	return isSubscriptionPoolError(err) || credentials.SubscriptionLaneRefused(err) ||
		codexSubscriptionModelRejected(err) || anthropicSubscriptionModelRejected(err) ||
		codexOAuthCredentialRejected(err) || anthropicOAuthCredentialRejected(err) || anthropicSubscriptionThirdPartyRefused(err)
}

func strikeModelUnlessFundingFailure(ctx context.Context, model string, err error) {
	if subscriptionFundingFailure(err) {
		return
	}
	recordTurnFailure(ctx, model)
}
