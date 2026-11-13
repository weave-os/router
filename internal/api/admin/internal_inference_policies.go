package admin

import (
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/policy"

	"github.com/gin-gonic/gin"
)

// InternalInferencePoliciesHandler serves the static registry projection: the
// reviewed policy for every purpose, identical to the generated docs.
func InternalInferencePoliciesHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, proxySvc.InferencePolicyRegistry().StaticProjection())
	}
}

// InternalInferenceDeploymentHandler serves the registry as this deployment
// can serve it: configured provider names and per-purpose candidate bindings.
func InternalInferenceDeploymentHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, proxySvc.InferenceDeploymentProjection())
	}
}

// InternalInferenceResolveHandler previews plan resolution for one purpose.
// Typed resolver failures come back as 422 with a stable code; anything else
// is reported without detail.
func InternalInferenceResolveHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req policy.InspectionRequest
		if err := c.ShouldBindJSON(&req); err != nil || len(req.Purpose) == 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "A purpose is required."})
			return
		}
		plan, err := proxySvc.InspectInferencePlan(req)
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
