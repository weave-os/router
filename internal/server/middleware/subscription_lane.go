package middleware

import (
	"weave-os/router/internal/billing"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/subscriptionlane"

	"github.com/gin-gonic/gin"
)

func tripSubscriptionLane(c *gin.Context, gate subscriptionlane.Gate, subscriptionCovers bool) (refusal.Kind, bool) {
	ctx, lane := billing.StepSubscriptionLane(c.Request.Context(), gate.Tripped(subscriptionCovers))
	kind, refused := lane.Refusal()
	if !refused {
		c.Request = c.Request.WithContext(ctx)
	}
	return kind, refused
}
