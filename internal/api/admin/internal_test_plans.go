package admin

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
)

func testPlanFailure(c *gin.Context, err error) {
	observability.FromGin(c).Warn("Internal test plan operation refused", "method", c.Request.Method, "err", err)
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "Test identity, budget or pinned selection is unavailable. Preview a new launch."})
}

// InternalTestPlanIdentitiesHandler lists funded eligible subjects behind internal service auth.
func InternalTestPlanIdentitiesHandler(tools *policyregistry.TestPlanTools) gin.HandlerFunc {
	return func(c *gin.Context) {
		identities, err := tools.Repository.ListTestIdentities(c.Request.Context())
		if err != nil {
			testPlanFailure(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, identities)
	}
}

// InternalTestPlanPreviewHandler resolves an exact production selection without inference.
func InternalTestPlanPreviewHandler(tools *policyregistry.TestPlanTools) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			SubjectID string                  `json:"subject_id"`
			Plan      policyregistry.TestPlan `json:"plan"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid test plan request."})
			return
		}
		preview, err := tools.Preview(c.Request.Context(), request.SubjectID, request.Plan)
		if err != nil {
			testPlanFailure(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, preview)
	}
}

// InternalTestPlanPrepareHandler prepares a confirmed, expiring bearer grant.
func InternalTestPlanPrepareHandler(tools *policyregistry.TestPlanTools) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request struct {
			Preview   policyregistry.TestPlanPreview `json:"preview"`
			Confirmed bool                           `json:"confirmed"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid test plan request."})
			return
		}
		configuration, err := tools.Prepare(c.Request.Context(), request.Preview, request.Confirmed)
		if err != nil {
			testPlanFailure(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, configuration)
	}
}

// InternalTestPlanRevokeHandler revokes only the ephemeral launch grant.
func InternalTestPlanRevokeHandler(tools *policyregistry.TestPlanTools) gin.HandlerFunc {
	return func(c *gin.Context) {
		err := tools.Repository.RevokeTestLaunch(c.Request.Context(), c.Param("id"))
		if err != nil {
			if errors.Is(err, policyregistry.ErrTestLaunchNotFound) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "test_launch_not_found"})
				return
			}
			testPlanFailure(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// TestPlanValidationHandler returns the verified scope after the worker snapshot loads.
func TestPlanValidationHandler(c *gin.Context) {
	assertion := policyregistry.ServingAssertionFromContext(c.Request.Context())
	if assertion == nil || assertion.TestPlan == nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "test_launch_required"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"test_plan": assertion.TestPlan, "admission": assertion.Admission})
}
