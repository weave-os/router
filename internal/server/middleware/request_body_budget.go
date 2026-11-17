package middleware

import (
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

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
	RequestBodyReadTimeout             = 2 * time.Minute
)

func WithRequestBodyBudget(budgetBytes int64, readTimeout time.Duration) gin.HandlerFunc {
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
		limitBodyReadTime(c, readTimeout)
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

func limitBodyReadTime(c *gin.Context, readTimeout time.Duration) {
	controller := http.NewResponseController(c.Writer)
	if controller.SetReadDeadline(time.Now().Add(readTimeout)) != nil {
		return
	}
	c.Request.Body = &deadlineClearingBody{ReadCloser: c.Request.Body, controller: controller}
}

type deadlineClearingBody struct {
	io.ReadCloser
	controller *http.ResponseController
	once       sync.Once
}

func (b *deadlineClearingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		b.clearDeadline()
	}
	return n, err
}

func (b *deadlineClearingBody) Close() error {
	b.clearDeadline()
	return b.ReadCloser.Close()
}

func (b *deadlineClearingBody) clearDeadline() {
	b.once.Do(func() { b.controller.SetReadDeadline(time.Time{}) })
}
