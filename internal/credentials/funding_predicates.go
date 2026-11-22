package credentials

import (
	"context"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptionlane"
)

func CustomerFunded(creds *requestcontext.Credentials) bool {
	if creds == nil || creds.CredentialError != nil || len(creds.APIKey) == 0 {
		return false
	}
	return creds.OAuth || creds.Source == requestcontext.SourceBYOK || creds.Source == requestcontext.SourceClient
}

func CustomerKeyFunded(creds *requestcontext.Credentials) bool {
	return CustomerFunded(creds) && creds.Source == requestcontext.SourceBYOK
}

func UnfundedKeyFunds(ctx context.Context, creds *requestcontext.Credentials) bool {
	return CustomerKeyFunded(creds) && !billing.ByokFeeUnpayable(ctx)
}

func QuotaProofExempt(ctx context.Context, creds *requestcontext.Credentials) bool {
	return creds != nil && creds.OAuth || PaidCapacityUnfunded(ctx) && UnfundedKeyFunds(ctx, creds)
}

func PaidCapacityUnfunded(ctx context.Context) bool {
	return !subscriptionlane.Permits(billing.SubscriptionLaneFromContext(ctx), subscriptionlane.OrganizationCreditsServed)
}
