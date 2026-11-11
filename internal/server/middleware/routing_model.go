package middleware

import (
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ModelPassthroughHeader opts a client into serving any requested model that
// is not a Weave routing model as-is, so only an explicit routing model routes.
const ModelPassthroughHeader = "X-Weave-Model-Passthrough"

// applyRoutingModel runs after classifier admission and reuses its parsed body.
func applyRoutingModel(c *gin.Context, body []byte, available StrategyAvailability) bool {
	model := router.RoutingModel(requestedModel(c, body))
	if !router.IsRoutingModel(string(model)) {
		if strings.HasPrefix(string(model), "weave-") {
			observability.FromGin(c).Debug("Unknown routing model rejected")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "unknown_routing_model"})
			return false
		}
		if model != "" && c.GetHeader(ModelPassthroughHeader) == "1" {
			ctx := auth.WithClientModelPassthrough(c.Request.Context())
			c.Request = c.Request.WithContext(ctx)
			observability.FromGin(c).Debug("Client-selected model served without routing", "model", model)
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
	selection, _ := router.RoutingModelPolicyFor(model)
	identity, managed := requestcontext.ServingIdentityFromContext(c.Request.Context())
	internal := managed && policyregistry.ExperimentalRoutingModelsPermitted(policyregistry.ServingTarget(identity.Target), entitlement.Plan(identity.Plan)) && identity.ReleaseID != "" && identity.BindingID != "" && identity.CredentialIdentity != ""
	if (managed || model == router.ModelQwen) && !internal {
		observability.FromGin(c).Debug("Routing model is unavailable for this serving scope", "model", model, "serving_target", identity.Target)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "routing_model_not_permitted"})
		return false
	}
	selectedStrategy := selection.Strategy
	if !routingStrategyServable(c, managed, selectedStrategy, available) {
		if !selection.FallsBackToPrimary || !routingStrategyServable(c, managed, router.PrimaryRoutingStrategy, available) {
			observability.FromGin(c).Warn("Routing model has no servable policy", "model", model, "strategy", selectedStrategy)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": selection.UnavailableError})
			return false
		}
		observability.FromGin(c).Info("Routing model variant is absent; serving the primary classifier", "model", model, "strategy", selectedStrategy, "fallback_strategy", router.PrimaryRoutingStrategy)
		selectedStrategy = router.PrimaryRoutingStrategy
	}
	c.Request = c.Request.WithContext(router.WithStrategy(c.Request.Context(), selectedStrategy))
	c.Request = c.Request.WithContext(router.WithRoutingModel(c.Request.Context(), model))
	observability.FromGin(c).Debug("Routing model selected", "model", model)
	return true
}

// routingStrategyServable requires managed strategies to exist in the admitted
// snapshot; the decision model is configured outside serving releases.
func routingStrategyServable(c *gin.Context, managed bool, strategy router.Strategy, available StrategyAvailability) bool {
	if managed && strategy != router.StrategyDecisionModel {
		snapshot := policyregistry.ServingSnapshotFromContext(c.Request.Context())
		if snapshot == nil || snapshot.Routers[strategy] == nil {
			return false
		}
	}
	return available != nil && available(strategy)
}

// requestedModel follows Gemini's authoritative URL model; its handler replaces
// body.model before dispatch. Other protocols select through the body field.
func requestedModel(c *gin.Context, body []byte) string {
	if model, _, _ := strings.Cut(c.Param("modelAction"), ":"); model != "" {
		return model
	}
	return gjson.GetBytes(body, "model").String()
}
