package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// WithSubscriberAllowance gates inference on an individual Max/Boost
// subscriber's included Router allowance. Attached after WithAuth so the
// authenticated credential subject below is populated.
//
// A request with no individual entitlement passes through untouched: Enterprise
// organization billing, BYOK, and prepaid keys keep the gates they already had,
// and this middleware is the only place the included allowance is enforced.
//
// An exhausted window answers 402 rather than silently routing onto paid
// capacity — the subscriber bought a bounded allowance, and quietly spending
// their org's balance instead is the surprise this gate exists to prevent. An
// allowance read error fails closed with 503, mirroring WithBalanceCheck: an
// allowance that admits everything while unreadable is an unbilled-usage hole.
func WithSubscriberAllowance(svc *entitlement.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		apiKey := APIKeyFrom(c)
		if apiKey == nil || apiKey.CredentialSubjectID == "" {
			c.Next()
			return
		}

		subscriberID := entitlement.SubscriberID(apiKey.CredentialSubjectID)

		admission, err := svc.Admit(c.Request.Context(), subscriberID)
		if err != nil {
			log.Error("Subscriber allowance check failed; refusing request", "err", err, "subscriber_id", apiKey.CredentialSubjectID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}

		// The plan's hard model boundary is stamped before the allowance
		// verdict is acted on, so it governs the turn no matter which book
		// ends up paying for it — included allowance, prepaid, or the caller's
		// own covering subscription.
		if admission.Plan != "" {
			c.Request = c.Request.WithContext(entitlement.WithProductScope(c.Request.Context(), admission.Plan))
		}

		// An agent-shadow evaluation draws no included allowance — it is Weave's
		// own traffic, not the subscriber's turn — but it dispatches a forced
		// model, so it runs after the boundary above is stamped.
		if _, shadow := proxy.AgentShadowEvalFromContext(c.Request.Context()); shadow {
			c.Next()
			return
		}

		switch admission.Outcome {
		case entitlement.AdmissionNotSubscribed:
			c.Next()
		case entitlement.AdmissionExhausted:
			// A request presenting a Claude/Codex credential covering this route
			// can serve at $0 on the caller's own plan without drawing included
			// Router capacity, so a spent allowance must not refuse it. Settlement
			// stays honest either way: it accounts only included_router capacity,
			// and this request carries no coverage to settle against.
			if serveOnCoveringSubscription(c) {
				return
			}
			window := exhaustedWindow(admission)
			log.Info("Request rejected: subscriber allowance exhausted",
				"subscriber_id", apiKey.CredentialSubjectID,
				"period_kind", admission.ExhaustedPeriod,
				"consumed_usd_micros", window.ConsumedUsdMicros(),
				"allowance_usd_micros", window.LimitUsdMicros,
			)
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
				"error":                "subscription_allowance_exhausted",
				"period_kind":          string(admission.ExhaustedPeriod),
				"period_end":           window.Period.End,
				"consumed_usd_micros":  window.ConsumedUsdMicros(),
				"allowance_usd_micros": window.LimitUsdMicros,
				"message":              allowanceExhaustedMessage(admission.ExhaustedPeriod),
			})
		case entitlement.AdmissionCovered:
			holdRequest(c, log, svc, admission)
		}
	}
}

// holdRequest reserves an upper-bound turn cost before the request is
// dispatched, and returns the hold once it has been served.
//
// The admission check above cannot enforce the allowance on its own: it reads
// the windows, so concurrent turns all see the same headroom and all pass. The
// reservation accrues and checks in one atomic write, which is what keeps
// consumed + reserved within the limit. Settlement books the turn's actual
// cost under its own action identifiers, so releasing the hold afterwards
// neither refunds nor double-charges the served work.
func holdRequest(c *gin.Context, log *slog.Logger, svc *entitlement.Service, admission entitlement.Admission) {
	ctx := c.Request.Context()
	requestID := observability.RequestIDFromContext(ctx)
	if requestID == "" {
		// The identifier only has to be unique per delivery for the hold to be
		// releasable; correlating it with the request's logs is a bonus.
		requestID = uuid.NewString()
	}

	hold := entitlement.Hold{
		Coverage:            admission.Coverage,
		ActionID:            requestID + holdActionSuffix,
		RouterRequestID:     requestID,
		APIKeyID:            APIKeyFrom(c).ID,
		RequestedModel:      entitlement.ModelUnresolved,
		UpperBoundUsdMicros: holdUsdMicros(admission.Usage),
		CapacitySource:      entitlement.CapacitySourceIncludedRouter,
	}

	if _, err := svc.Reserve(ctx, hold); errors.Is(err, entitlement.ErrAllowanceExhausted) {
		var exhausted entitlement.ExhaustedError
		errors.As(err, &exhausted)
		refuseExhausted(c, log, admission, exhausted.Period)
		return
	} else if err != nil {
		log.Error("Subscriber allowance reservation failed; refusing request", "err", err, "subscriber_id", string(admission.Coverage.SubscriberID))
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error":   "billing_unavailable",
			"message": "Billing system is temporarily unavailable. Retry in a few moments.",
		})
		return
	}

	// Coverage is stamped only once the hold is confirmed: a refused request
	// must not reach settlement as allowance-covered.
	c.Request = c.Request.WithContext(entitlement.WithCoverage(ctx, admission.Coverage))

	c.Next()

	// The release outlives the request: a client that disconnects mid-turn
	// cancels ctx, and releasing under it would leave the bound held for the
	// rest of the window on every abandoned request.
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseHoldTimeout)
	defer cancel()
	if err := svc.ReleaseHold(releaseCtx, hold.ActionID); err != nil {
		// The bound stays held until the window turns. That over-counts the
		// subscriber's usage, which is the safe direction: the alternative is
		// serving work the allowance may not cover.
		log.Error("Subscriber allowance hold left standing", "err", err, "action_id", hold.ActionID)
	}
}

// holdActionSuffix distinguishes the request-level hold from the per-action
// identifiers settlement mints for the same request.
const holdActionSuffix = ":hold"

// releaseHoldTimeout bounds the detached release so a stalled accounting write
// cannot pin the served request's goroutine.
const releaseHoldTimeout = 5 * time.Second

// holdUsdMicros bounds one turn's cost, clamped to the headroom the tightest
// window still has. Clamping to the limit instead would refuse every turn once
// a window carries any usage, and a subscriber whose remaining allowance is
// smaller than one worst-case turn could never dispatch at all.
func holdUsdMicros(usage entitlement.Usage) int64 {
	bound := catalog.TurnUpperBoundUsdMicros()
	for _, window := range []entitlement.WindowUsage{usage.SixHour, usage.Billing} {
		headroom := window.LimitUsdMicros - window.ConsumedUsdMicros()
		if headroom > 0 && headroom < bound {
			bound = headroom
		}
	}
	return bound
}

// refuseExhausted answers a spent window, unless the caller's own linked
// subscription can serve the route at no cost to the included allowance.
func refuseExhausted(c *gin.Context, log *slog.Logger, admission entitlement.Admission, period entitlement.PeriodKind) {
	if serveOnCoveringSubscription(c) {
		return
	}
	admission.ExhaustedPeriod = period
	window := exhaustedWindow(admission)
	log.Info("Request rejected: subscriber allowance exhausted",
		"subscriber_id", string(admission.Coverage.SubscriberID),
		"period_kind", period,
		"consumed_usd_micros", window.ConsumedUsdMicros(),
		"allowance_usd_micros", window.LimitUsdMicros,
	)
	c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
		"error":                "subscription_allowance_exhausted",
		"period_kind":          string(period),
		"period_end":           window.Period.End,
		"consumed_usd_micros":  window.ConsumedUsdMicros(),
		"allowance_usd_micros": window.LimitUsdMicros,
		"message":              allowanceExhaustedMessage(period),
	})
}

// serveOnCoveringSubscription serves a turn the caller's own linked plan
// covers, and reports whether it did. The turn is marked subscription-only, as
// the balance and spend-cap gates mark theirs: without it routing stays free to
// fall back onto paid capacity, which is the spend a spent allowance refuses.
func serveOnCoveringSubscription(c *gin.Context) bool {
	if !proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath()) {
		return false
	}
	c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context()))
	c.Next()
	return true
}

// subscriberAllowanceCovers reports whether this request was admitted against
// an individual Max/Boost allowance. Such a turn debits 0 on the organization
// balance and settles against the subscriber's allowance instead, so the
// organization's prepaid and spend-cap gates do not apply to it.
func subscriberAllowanceCovers(c *gin.Context) bool {
	_, covered := entitlement.CoverageFromContext(c.Request.Context())
	return covered
}

// exhaustedWindow returns the usage of the window that rejected the request.
func exhaustedWindow(admission entitlement.Admission) entitlement.WindowUsage {
	if admission.ExhaustedPeriod == entitlement.PeriodKindSixHour {
		return admission.Usage.SixHour
	}
	return admission.Usage.Billing
}

func allowanceExhaustedMessage(kind entitlement.PeriodKind) string {
	if kind == entitlement.PeriodKindSixHour {
		return "This subscription's six-hour usage allowance is spent. Usage resets at the end of the current window."
	}
	return "This subscription's monthly usage allowance is spent. Usage resets when the billing period renews."
}
