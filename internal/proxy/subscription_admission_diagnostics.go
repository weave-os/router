package proxy

import (
	"context"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

type subscriptionAccountSkipReason string

const (
	subscriptionSkipRepeatedLease    subscriptionAccountSkipReason = "repeated_lease"
	subscriptionSkipOutsideAdmission subscriptionAccountSkipReason = "outside_admission_snapshot"
	subscriptionSkipClientAppDenied  subscriptionAccountSkipReason = "client_app_denied"
	subscriptionSkipOverloadCooldown subscriptionAccountSkipReason = "overload_cooldown"
	subscriptionSkipModelDenied      subscriptionAccountSkipReason = "model_denied"
	subscriptionSkipPlanIneligible   subscriptionAccountSkipReason = "plan_ineligible"
	subscriptionSkipPaidOverage      subscriptionAccountSkipReason = "paid_overage"
	subscriptionSkipQuotaExhausted   subscriptionAccountSkipReason = "quota_exhausted"
)

type subscriptionAccountSkip struct {
	AccountID string                        `json:"account_id"`
	Tier      auth.SubscriptionTier         `json:"tier"`
	Reason    subscriptionAccountSkipReason `json:"reason"`
}

type subscriptionAdmissionDiagnostics struct {
	started  time.Time
	skipped  int
	reasons  map[subscriptionAccountSkipReason]int
	accounts []subscriptionAccountSkip
	admitted bool
}

func (d *subscriptionAdmissionDiagnostics) skip(lease subscriptions.Lease, reason subscriptionAccountSkipReason) {
	d.skipped++
	if d.reasons == nil {
		d.reasons = make(map[subscriptionAccountSkipReason]int)
	}
	d.reasons[reason]++
	if len(d.accounts) < 16 {
		d.accounts = append(d.accounts, subscriptionAccountSkip{AccountID: lease.AccountID, Tier: lease.Tier, Reason: reason})
	}
}

func (d *subscriptionAdmissionDiagnostics) log(ctx context.Context, provider subscriptions.Provider, model string) {
	if d.skipped == 0 {
		return
	}
	// The requested model may contain caller content; emit only a catalog ID.
	canonical, _ := catalog.ByID(model)
	observability.FromContext(ctx).Info("Subscription account admission evaluated", "subscription_event", auth.SubscriptionCredentialEventAdmissionEvaluated, "provider", provider, "model", canonical.ID, "account_admitted", d.admitted, "skipped_count", d.skipped, "skip_reasons", d.reasons, "skipped_accounts", d.accounts, "skipped_accounts_truncated", d.skipped > len(d.accounts), "admission_ms", time.Since(d.started).Milliseconds())
}
