package middleware

import (
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const ModelPassthroughHeader = "X-Weave-Model-Passthrough"

func applyRoutingModel(c *gin.Context, body []byte, available StrategyAvailability) bool {
	model := router.CanonicalRoutingModel(requestedModel(c, body))
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
	if c.GetHeader(requestcontext.ForceModelHeader) != "" {
		observability.FromGin(c).Debug("Routing model conflicts with direct model pin", "model", model)
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "routing_model_conflicts_with_force_model"})
		return false
	}
	identity, managed := requestcontext.ServingIdentityFromContext(c.Request.Context())
	scope := router.RoutingModelScope{
		Managed:  managed,
		Internal: managed && policyregistry.ExperimentalRoutingModelsPermitted(policyregistry.ServingTarget(identity.Target), entitlement.Plan(identity.Plan)) && identity.ReleaseID != "" && identity.BindingID != "" && identity.CredentialIdentity != "",
	}
	choice := router.SelectRoutingModel(router.StrategySelectionFromContext(c.Request.Context()), model, scope, func(strategy router.Strategy) bool {
		return routingStrategyServable(c, managed, strategy, available)
	})
	switch choice.Outcome {
	case router.RoutingModelServed:
	case router.RoutingModelClassifierConflict:
		observability.FromGin(c).Debug("Routing model conflicts with enrolled classifier thread", "model", model)
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "start_new_thread_to_change_router"})
		return false
	case router.RoutingModelNotPermitted:
		observability.FromGin(c).Debug("Routing model is unavailable for this serving scope", "model", model, "serving_target", identity.Target)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "routing_model_not_permitted"})
		return false
	case router.RoutingModelUnservable:
		observability.FromGin(c).Warn("Routing model has no servable policy", "model", model, "strategy", choice.PolicyStrategy)
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": choice.UnavailableError})
		return false
	case router.RoutingModelPrimaryFallback:
		observability.FromGin(c).Info("Routing model variant is absent; serving the primary classifier", "model", model, "strategy", choice.PolicyStrategy, "fallback_strategy", choice.Selection.Strategy)
	}
	c.Request = c.Request.WithContext(router.WithStrategySelection(c.Request.Context(), choice.Selection))
	observability.FromGin(c).Debug("Routing model selected", "model", model)
	return true
}

func routingStrategyServable(c *gin.Context, managed bool, strategy router.Strategy, available StrategyAvailability) bool {
	if managed && router.RequiresPolicySnapshot(strategy) {
		snapshot := policyregistry.ServingSnapshotFromContext(c.Request.Context())
		if snapshot == nil || snapshot.Routers[strategy] == nil {
			return false
		}
	}
	return available != nil && available(strategy)
}

func requestedModel(c *gin.Context, body []byte) string {
	if model, _, _ := strings.Cut(c.Param("modelAction"), ":"); model != "" {
		return model
	}
	return gjson.GetBytes(body, "model").String()
}
