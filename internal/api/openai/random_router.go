package openai

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	randomrouter "weave-os/router/internal/router/random_router"
)

func RandomRouterIdentityHandler(svc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, config, err := proxy.BeginRandomBenchmark(svc, c.Request.Context(), randomrouter.PolicySHA256)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "random_model_not_admitted"})
			return
		}
		ctx, release, err := proxy.PrepareRandomBenchmark(svc, ctx)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "random_model_capacity_unavailable"})
			return
		}
		defer release()
		serving, _ := requestcontext.ServingIdentityFromContext(ctx)
		keyID, _ := ctx.Value(proxy.APIKeyIDContextKey{}).(string)
		state := randomrouter.RequestFrom(ctx)
		c.JSON(http.StatusOK, gin.H{
			"model":                    router.ModelRandom,
			"api_key_id":               keyID,
			"capacity_account_id":      state.CapacityAccountID,
			"policy_id":                randomrouter.PolicyID,
			"policy_sha256":            config.PolicySHA256,
			"config":                   config,
			"serving":                  serving,
			"models":                   randomrouter.Models(),
			"propensity":               1.0 / 3.0,
			"funding_pool_id":          config.InstallationID,
			"funding_policy":           "included_codex_pool.v1",
			"retry_policy":             "hosted_same_model.v1",
			"max_same_binding_retries": dispatch.MaxSameBindingRetries,
			"capacity_observed_at":     time.Now().UTC(),
			"capacity_ready":           true,
			"strict":                   true,
		})
	}
}
