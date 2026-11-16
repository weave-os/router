package openai

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	randomrouter "weave-os/router/internal/router/random_router"
)

func RandomRouterGate(svc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Session-Id") == "" || c.GetHeader("x-weave-force-model") != "" || c.GetHeader("x-weave-force-cluster") != "" || c.GetHeader("x-weave-policy-pin") != "" || c.GetHeader("x-weave-model-passthrough") != "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "random_benchmark_invalid_headers"})
			return
		}
		ctx, config, err := proxy.BeginRandomBenchmark(svc, c.Request.Context(), c.GetHeader(randomrouter.PolicyHeader))
		if err != nil {
			observability.FromGin(c).Warn("Random benchmark rejected", "err", err)
			c.Header("x-should-retry", "false")
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "random_benchmark_not_admitted"})
			return
		}
		if c.GetHeader("X-Weave-Random-Subscription-Account") != config.SubscriptionAccountID {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "random_benchmark_subscription_changed"})
			return
		}
		c.Header("X-Weave-Random-Request-Id", observability.RequestIDFromContext(ctx))
		serving, _ := requestcontext.ServingIdentityFromContext(ctx)
		c.Header("X-Weave-Random-Release-Id", serving.ReleaseID)
		c.Header("X-Weave-Random-Binding-Id", serving.BindingID)
		if c.Request.URL.Path == randomrouter.ResponsesPath {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, proxy.MaxRequestBodyBytes+1))
			if err != nil || len(body) > proxy.MaxRequestBodyBytes || proxy.ValidateRandomBenchmarkInput(ctx, body, c.Request.Header) != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "random_benchmark_invalid_input"})
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}
		c.Header(randomrouter.PolicyHeader, config.PolicySHA256)
		ctx, release, err := proxy.PrepareRandomBenchmark(svc, ctx)
		if err != nil {
			c.Header("x-should-retry", "false")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "random_benchmark_capacity_unavailable"})
			return
		}
		defer release()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func RandomRouterIdentityHandler(svc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, config, err := proxy.BeginRandomBenchmark(svc, c.Request.Context(), c.GetHeader(randomrouter.PolicyHeader))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "random_benchmark_not_admitted"})
			return
		}
		serving, _ := requestcontext.ServingIdentityFromContext(ctx)
		c.JSON(http.StatusOK, gin.H{"serving": serving, "policy_id": randomrouter.PolicyID, "config": config, "models": randomrouter.Models(), "strict": true})
	}
}
