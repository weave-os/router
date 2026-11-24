package admin

import (
	"net/http"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/policy"

	"github.com/gin-gonic/gin"
)

func InternalInferencePoliciesHandler(plans dispatch.InferencePlanInspector) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, plans.InferencePolicyRegistry().StaticProjection())
	}
}

func InternalInferenceDeploymentHandler(plans dispatch.InferencePlanInspector) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, plans.InferenceDeploymentProjection())
	}
}

func InternalInferenceResolveHandler(plans dispatch.InferencePlanInspector) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req policy.InspectionRequest
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Purpose) == 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "A purpose is required."})
			return
		}
		plan, err := plans.InspectInferencePlan(req)
		if err != nil {
			if projected, ok := policy.ProjectResolutionError(err); ok {
				c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": projected})
				return
			}
			observability.FromGin(c).Error("Inference plan inspection failed", "purpose", req.Purpose, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve inference plan."})
			return
		}
		c.JSON(http.StatusOK, plan.Projection())
	}
}
