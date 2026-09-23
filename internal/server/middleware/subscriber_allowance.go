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

	"github.com/cenkalti/backoff/v5"
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
// Every caller prefers compatible linked-provider capacity, on either plan and
// on API pricing: a turn the caller's own Claude/Codex plan covers serves at $0
// there before any metered capacity is drawn. When linked and included capacity
// are unavailable, requests continue through the existing organization balance
// and spend-limit gates. Allowance read errors fail closed because treating an
// unreadable meter as exhausted would incorrectly authorize organization
// spending.
func WithSubscriberAllowance(svc *entitlement.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		// The allowance follows the person the request identified itself as,
		// not the key it authenticated with: a key shared across an
		// organization must spend each caller's own included capacity.
		owner := SubscriptionOwnerFrom(c)
		if owner.SubscriberID == "" {
			c.Next()
			return
		}

		subscriberID := entitlement.SubscriberID(owner.SubscriberID)

		admission, err := svc.Admit(c.Request.Context(), subscriberID)
		if err != nil {
			log.Error("Subscriber allowance check failed; refusing request", "err", err, "subscriber_id", subscriberID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "billing_unavailable",
				"message": "Billing system is temporarily unavailable. Retry in a few moments.",
			})
			return
		}

		// The plan's hard model boundary is stamped before the allowance
		// verdict is acted on, so it governs the turn no matter which book
		// ends up paying for it — included allowance, organization credits, or the caller's
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

		// Linked-provider-first funding, independent of the admission verdict: a
		// request presenting a Claude/Codex credential covering this route serves
		// at $0 on the caller's own plan, so capacity the subscriber (or their
		// organization) pays for is spent only once that plan cannot take the
		// turn. Marking the request subscription-only prevents a provider failure
		// from silently changing the funding source to metered capacity.
		// Settlement stays honest: it accounts only included_router capacity, and
		// this request carries no coverage to settle against.
		if serveOnCoveringSubscription(c) {
			return
		}

		if admission.Outcome == entitlement.AdmissionNotSubscribed ||
			(admission.Outcome == entitlement.AdmissionExhausted && !admission.HeldCapacityOnly()) {
			c.Next()
			return
		}
		holdRequest(c, log, svc, subscriberID, admission)
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
func holdRequest(c *gin.Context, log *slog.Logger, svc *entitlement.Service, subscriberID entitlement.SubscriberID, admission entitlement.Admission) {
	ctx := c.Request.Context()
	requestID := observability.RequestIDFromContext(ctx)
	if requestID == "" {
		// The identifier only has to be unique per delivery for the hold to be
		// releasable; correlating it with the request's logs is a bonus.
		requestID = uuid.NewString()
	}

	retryPolicy := backoff.NewExponentialBackOff()
	retryPolicy.InitialInterval = 40 * time.Millisecond
	retryPolicy.MaxInterval = 250 * time.Millisecond
	firstAttempt := true
	hold, err := backoff.Retry(ctx, func() (entitlement.Hold, error) {
		if !firstAttempt {
			var readErr error
			admission, readErr = svc.Admit(ctx, subscriberID)
			if readErr != nil {
				return entitlement.Hold{}, backoff.Permanent(readErr)
			}
		}
		firstAttempt = false
		if admission.Outcome == entitlement.AdmissionNotSubscribed ||
			(admission.Outcome == entitlement.AdmissionExhausted && !admission.HeldCapacityOnly()) {
			return entitlement.Hold{}, nil
		}
		if admission.Outcome == entitlement.AdmissionExhausted {
			return entitlement.Hold{}, entitlement.ErrAllowanceExhausted
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
		if _, reserveErr := svc.Reserve(ctx, hold); reserveErr != nil {
			if errors.Is(reserveErr, entitlement.ErrAllowanceExhausted) {
				return entitlement.Hold{}, reserveErr
			}
			return entitlement.Hold{}, backoff.Permanent(reserveErr)
		}
		return hold, nil
	}, backoff.WithBackOff(retryPolicy), backoff.WithMaxElapsedTime(2*time.Second))
	if errors.Is(err, entitlement.ErrAllowanceExhausted) {
		log.Warn("Subscriber allowance temporarily held by concurrent turns", "subscriber_id", subscriberID, "period", admission.ExhaustedPeriod)
		c.Header("Retry-After", "1")
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error":   "subscription_capacity_busy",
			"message": "Subscription capacity is temporarily reserved by another request. Retry shortly.",
		})
		return
	}
	if err != nil {
		if ctx.Err() != nil {
			c.Abort()
			return
		}
		log.Error("Subscriber allowance reservation failed; refusing request", "err", err, "subscriber_id", subscriberID)
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error":   "billing_unavailable",
			"message": "Billing system is temporarily unavailable. Retry in a few moments.",
		})
		return
	}
	if admission.Plan != "" {
		c.Request = c.Request.WithContext(entitlement.WithProductScope(c.Request.Context(), admission.Plan))
	}
	if hold.ActionID == "" {
		c.Next()
		return
	}

	// Coverage is stamped only once the hold is confirmed: a refused request
	// must not reach settlement as allowance-covered.
	hold.Coverage.ProjectedUsdMicros = hold.UpperBoundUsdMicros
	c.Request = c.Request.WithContext(entitlement.WithCoverage(c.Request.Context(), hold.Coverage))

	c.Next()
	if entitlement.SettlementFailed(c.Request.Context()) {
		log.Error("Subscriber allowance hold left standing after settlement failure", "action_id", hold.ActionID)
		return
	}

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
	for _, window := range []entitlement.WindowUsage{usage.SixHour, usage.Weekly, usage.Billing} {
		headroom := window.LimitUsdMicros - window.ConsumedUsdMicros()
		if headroom > 0 && headroom < bound {
			bound = headroom
		}
	}
	return bound
}

// serveOnCoveringSubscription serves a turn the caller's own linked plan
// covers, and reports whether it did. The turn is marked subscription-only so
// an upstream failure cannot skip linked-first ordering and spend organization
// credits instead.
func serveOnCoveringSubscription(c *gin.Context) bool {
	if !proxy.RequestPresentsCoveringSubscription(c.Request.Context(), c.Request.Header, c.FullPath()) {
		return false
	}
	c.Request = c.Request.WithContext(billing.WithSubscriptionOnly(c.Request.Context(), billing.SubscriptionOnlyLinkedFirst))
	c.Next()
	return true
}

// subscriberAllowanceCovers reports whether subscriber-owned capacity pays for
// this turn, so organization billing gates must not inspect it.
func subscriberAllowanceCovers(c *gin.Context) bool {
	if _, covered := entitlement.CoverageFromContext(c.Request.Context()); covered {
		return true
	}
	return false
}
