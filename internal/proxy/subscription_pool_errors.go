package proxy

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrSubscriptionPoolExhausted   = errors.New("subscription account pool exhausted")
	ErrSubscriptionPoolUnavailable = errors.New("subscription account pool unavailable")
	ErrSubscriptionPoolThrottled   = fmt.Errorf("%w: every blocking account is rate-limited", ErrSubscriptionPoolUnavailable)
	errCallerPlanThrottled         = fmt.Errorf("%w: caller plan is rate-limited", ErrSubscriptionPoolThrottled)
)

func isSubscriptionPoolError(err error) bool {
	return errors.Is(err, ErrSubscriptionPoolExhausted) || errors.Is(err, ErrSubscriptionPoolUnavailable) || errors.Is(err, ErrClaudeEnterpriseSubscriptionUnavailable)
}

func subscriptionFundingFailure(err error) bool {
	return isSubscriptionPoolError(err) || subscriptionLaneRefused(err) ||
		codexSubscriptionModelRejected(err) || anthropicSubscriptionModelRejected(err) ||
		codexOAuthCredentialRejected(err) || anthropicOAuthCredentialRejected(err) || anthropicSubscriptionThirdPartyRefused(err)
}

func strikeModelUnlessFundingFailure(ctx context.Context, model string, err error) {
	if subscriptionFundingFailure(err) {
		return
	}
	recordTurnFailure(ctx, model)
}
