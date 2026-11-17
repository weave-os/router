package middleware

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	randomrouter "weave-os/router/internal/router/random_router"
)

func WithRandomRoutingModel(service *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, proxy.MaxRequestBodyBytes+1))
		if err != nil || len(body) > proxy.MaxRequestBodyBytes {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request_body"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		if c.Request.URL.Path != "/v1/responses" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "random_model_surface_unavailable"})
			return
		}
		ctx, _, err := proxy.BeginRandomBenchmark(service, c.Request.Context(), c.GetHeader(randomrouter.PolicyHeader))
		if err != nil || proxy.ValidateRandomBenchmarkInput(ctx, body, c.Request.Header) != nil {
			c.Header("x-should-retry", "false")
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "random_model_not_admitted"})
			return
		}
		ctx, release, err := proxy.PrepareRandomBenchmark(service, ctx)
		if err != nil {
			c.Header("x-should-retry", "false")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "random_model_capacity_unavailable"})
			return
		}
		defer release()
		c.Header(randomrouter.PolicyHeader, randomrouter.PolicySHA256)
		identity, _ := requestcontext.ServingIdentityFromContext(ctx)
		c.Header("X-Weave-Random-Release-Id", identity.ReleaseID)
		c.Header("X-Weave-Random-Binding-Id", identity.BindingID)
		c.Header("X-Weave-Random-Request-Id", observability.RequestIDFromContext(ctx))
		c.Header("X-Weave-Random-Capacity-Ready", "true")
		c.Request = c.Request.WithContext(router.WithRoutingModel(ctx, router.ModelRandom))
		c.Next()
	}
}
