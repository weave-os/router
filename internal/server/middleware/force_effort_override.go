package middleware

import (
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

const ForceEffortOverrideHeader = "x-weave-effort"

func WithForceEffortOverride() gin.HandlerFunc {
	return func(c *gin.Context) {
		if identity, managed := requestcontext.ServingIdentityFromContext(c.Request.Context()); managed && entitlement.PlanHasServingProfile(entitlement.Plan(identity.Plan)) {
			c.Next()
			return
		}
		raw := strings.TrimSpace(c.GetHeader(ForceEffortOverrideHeader))
		if raw == "" {
			c.Next()
			return
		}
		if !router.IsValidEffort(raw) {
			abortInvalidKnob(c, ForceEffortOverrideHeader+" must be one of: low, medium, high, max, xhigh (or aliases fast/minimal/ultra).")
			return
		}
		canonical := router.CanonicalizeEffort(raw)
		merged := router.Overrides{ForceEffort: canonical}
		if existing := router.RoutingKnobsFromContext(c.Request.Context()); existing != nil {
			merged.Alpha = existing.Alpha
			merged.QualityBias = existing.QualityBias
			merged.SpeedWeight = existing.SpeedWeight
			merged.OutputCostRatio = existing.OutputCostRatio
			merged.ExpectedOutputTokens = existing.ExpectedOutputTokens
			merged.PerModelVerbosity = existing.PerModelVerbosity
		}
		ctx := router.WithRoutingKnobs(c.Request.Context(), &merged)
		c.Request = c.Request.WithContext(ctx)
		log := observability.FromGin(c)
		log.Debug(
			"Force-effort override applied",
			"override_force_effort", canonical,
		)
		c.Next()
	}
}
