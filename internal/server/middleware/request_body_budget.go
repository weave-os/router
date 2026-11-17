package middleware

import (
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/requestcontext"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/semaphore"
)

const (
	RequestBodyBudgetBytes             = 1 << 30
	requestBodyBudgetThresholdBytes    = 4 << 20
	requestBodyProcessingAmplification = 8
)

func WithRequestBodyBudget(budgetBytes int64) gin.HandlerFunc {
	budget := semaphore.NewWeighted(budgetBytes)
	return func(c *gin.Context) {
		cost := requestBodyBudgetCost(c.Request)
		if cost == 0 {
			c.Next()
			return
		}
		if !budget.TryAcquire(cost) {
			observability.FromGin(c).Warn("Request body budget exhausted", "content_length", c.Request.ContentLength, "budget_cost_bytes", cost)
			abortRefusal(c, refusal.RequestBodyBudgetExhausted, nil)
			return
		}
		defer budget.Release(cost)
		c.Next()
	}
}

func requestBodyBudgetCost(r *http.Request) int64 {
	if r.Method != http.MethodPost {
		return 0
	}
	size := r.ContentLength
	if size < 0 {
		size = requestcontext.MaxRequestBodyBytes
	}
	if size < requestBodyBudgetThresholdBytes {
		return 0
	}
	return min(size, requestcontext.MaxRequestBodyBytes+1) * requestBodyProcessingAmplification
}
