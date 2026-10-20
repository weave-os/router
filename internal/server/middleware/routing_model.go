package middleware

import (
	"net/http"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// applyRoutingModel runs after classifier admission and reuses its parsed body.
func applyRoutingModel(c *gin.Context, body []byte, available StrategyAvailability) bool {
	model := router.RoutingModel(requestedModel(c, body))
	if !router.IsRoutingModel(string(model)) {
		if strings.HasPrefix(string(model), "weave-") {
			observability.FromGin(c).Debug("Unknown routing model rejected")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "unknown_routing_model"})
			return false
		}
		return true
	}
	if c.GetHeader(proxy.ForceModelHeader) != "" {
		observability.FromGin(c).Debug("Routing model conflicts with direct model pin", "model", model)
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "routing_model_conflicts_with_force_model"})
		return false
	}
	if model == router.ModelAutomatic {
		c.Request = c.Request.WithContext(router.WithRoutingModel(c.Request.Context(), model))
		observability.FromGin(c).Debug("Routing model selected", "model", model)
		return true
	}
	if router.StrategyFromContext(c.Request.Context()) == router.StrategyLLMClassifier {
		observability.FromGin(c).Debug("Routing model conflicts with enrolled classifier thread", "model", model)
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "start_new_thread_to_change_router"})
		return false
	}
	var selectedStrategy router.Strategy
	var unavailableMessage string
	switch model {
	case router.ModelHMM:
		selectedStrategy = router.StrategyHMMBeta
		unavailableMessage = "hmm_model_unavailable"
	case router.ModelQwen:
		selectedStrategy = router.StrategyHMMQwen
		unavailableMessage = "qwen_model_unavailable"
	}
	identity, managed := requestcontext.ServingIdentityFromContext(c.Request.Context())
	internal := managed && identity.Target == string(policyregistry.TargetInternal) && identity.ReleaseID != "" && identity.BindingID != "" && identity.CredentialIdentity != "" && identity.Plan == ""
	if (managed || model == router.ModelQwen) && !internal {
		observability.FromGin(c).Debug("Routing model is unavailable for this serving scope", "model", model, "serving_target", identity.Target)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "routing_model_not_permitted"})
		return false
	}
	if managed {
		snapshot := policyregistry.ServingSnapshotFromContext(c.Request.Context())
		if snapshot == nil || snapshot.Routers[selectedStrategy] == nil {
			observability.FromGin(c).Debug("Routing model is absent from admitted snapshot", "model", model)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": unavailableMessage})
			return false
		}
	}
	if available == nil || !available(selectedStrategy) {
		observability.FromGin(c).Debug("Routing model has no available registered policy", "model", model)
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": unavailableMessage})
		return false
	}
	c.Request = c.Request.WithContext(router.WithStrategy(c.Request.Context(), selectedStrategy))
	c.Request = c.Request.WithContext(router.WithRoutingModel(c.Request.Context(), model))
	observability.FromGin(c).Debug("Routing model selected", "model", model)
	return true
}

// requestedModel follows Gemini's authoritative URL model; its handler replaces
// body.model before dispatch. Other protocols select through the body field.
func requestedModel(c *gin.Context, body []byte) string {
	if model, _, _ := strings.Cut(c.Param("modelAction"), ":"); model != "" {
		return model
	}
	return gjson.GetBytes(body, "model").String()
}
