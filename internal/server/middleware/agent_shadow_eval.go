package middleware

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
)

func WithAgentShadowEvaluation() gin.HandlerFunc {
	return func(c *gin.Context) {
		model := strings.TrimSpace(c.GetHeader(requestcontext.AgentShadowModelHeader))
		rollout := strings.TrimSpace(c.GetHeader(requestcontext.AgentShadowRolloutHeader))
		stateID := strings.TrimSpace(c.GetHeader(requestcontext.AgentShadowStateHeader))
		present := model != "" || rollout != "" || stateID != ""
		if _, ok := requestcontext.InternalTestIdentityFrom(c.Request.Context()); ok && present {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "test_shadow_eval_forbidden"})
			return
		}
		if !present {
			c.Next()
			return
		}
		if model == "" || rollout == "" || stateID == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "agent_shadow_eval_headers_incomplete"})
			return
		}
		installation := InstallationFrom(c)
		if installation == nil || !installation.PolicyHeaderOverridesEnabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "agent_shadow_eval_not_authorized"})
			return
		}
		ctx := context.WithValue(c.Request.Context(), proxy.AgentShadowEvalContextKey{}, proxy.AgentShadowEvaluation{
			Model: model, RolloutID: rollout, StateID: stateID,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
