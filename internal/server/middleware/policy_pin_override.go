package middleware

import (
	"weave-os/router/internal/observability"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
)

const PolicyPinOverrideHeader = "x-weave-policy-pin"

func WithPolicyPinOverride() gin.HandlerFunc {
	return func(c *gin.Context) {
		if identity, managed := requestcontext.ServingIdentityFromContext(c.Request.Context()); managed && entitlement.PlanHasServingProfile(entitlement.Plan(identity.Plan)) {
			c.Next()
			return
		}
		raw := c.GetHeader(PolicyPinOverrideHeader)
		if raw == "" {
			c.Next()
			return
		}
		installation := InstallationFrom(c)
		if installation == nil || !installation.PolicyHeaderOverridesEnabled {
			observability.FromGin(c).Debug("Policy pin ignored: installation is not authorized for policy headers")
			ctx := router.WithPolicyPinRequest(c.Request.Context(), router.PolicyPinRequest{})
			c.Request = c.Request.WithContext(ctx)
			c.Next()
			return
		}
		pin, err := router.ParsePolicyPin(raw)
		if err != nil {
			abortMalformedPolicyPin(c, err.Error())
			return
		}
		observability.FromGin(c).Info("Policy pin applied", "installation_id", installation.ID, "policy_pin", pin.String())
		ctx := router.WithPolicyPinRequest(c.Request.Context(), router.PolicyPinRequest{Pin: pin, Authorized: true})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func abortMalformedPolicyPin(c *gin.Context, message string) {
	abortRefusal(c, refusal.PolicyPinMalformed, gin.H{"message": message})
}
