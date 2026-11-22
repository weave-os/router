package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/subscriptionlane"
	"weave-os/router/internal/subscriptions"
)

func codexPlanSpentForTurn(ctx context.Context, headers http.Header, observer *usage.Observer, provider string, err error, ownLoginSpent bool) bool {
	if provider != providers.ProviderOpenAI || codexSubscriptionModelRejected(err) {
		return false
	}
	if _, spent := codexQuotaExhaustion(err); spent {
		return true
	}
	if observer != nil {
		if token, _ := presentSubscriptionTokens(ctx, headers); token != "" && observer.Rejected(observer.Key([]byte(token))) {
			return false
		}
	}
	return isSubscriptionPoolError(err) && ownLoginSpent
}

func dropFailedIncludedModels(includedModels []string, failed string, excluded map[string]struct{}, planSpent bool) ([]string, map[string]struct{}) {
	remaining := includedModels[:0]
	spent := make(map[string]struct{})
	for _, model := range includedModels {
		switch {
		case model == failed:
		case planSpent && codexSubscriptionCanAttemptModel(model):
			spent[model] = struct{}{}
		default:
			remaining = append(remaining, model)
		}
	}
	return remaining, candidates.MergeExcludedModels(excluded, spent)
}

func emptyPaidPoolErr(ctx context.Context, includedFailure error, now time.Time) error {
	if includedFailure == nil && seatsThrottled(ctx, subscriptions.ProviderCodex, now) {
		return credentials.SubscriptionPoolRefusal(ctx, subscriptionlane.SubscriptionPoolThrottled)
	}
	return includedFailureOr(includedFailure, router.ErrAllowlistEmptiesPool)
}

func codexSeatsCoverOrThrottled(ctx context.Context, covered bool, now time.Time) bool {
	return covered || seatsThrottled(ctx, subscriptions.ProviderCodex, now)
}
