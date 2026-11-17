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
	RequestBodyIdleTimeout             = 30 * time.Second
)

type RequestBodyReadLimits struct {
	Total time.Duration
	Idle  time.Duration
}

var DefaultRequestBodyReadLimits = RequestBodyReadLimits{Total: RequestBodyReadTimeout, Idle: RequestBodyIdleTimeout}

func WithRequestBodyBudget(budgetBytes int64, readLimits RequestBodyReadLimits) gin.HandlerFunc {
	budget := newRequestBodyBudget(budgetBytes)
	return func(c *gin.Context) {
		cost := requestBodyBudgetCost(c.Request)
		if cost == 0 {
			c.Next()
			return
		}
		holder := requestBodyBudgetHolder(c)
		if !budget.tryAcquire(holder, cost) {
			observability.FromGin(c).Warn("Request body budget exhausted", "content_length", c.Request.ContentLength, "budget_cost_bytes", cost)
			abortRefusal(c, refusal.RequestBodyBudgetExhausted, nil)
			return
		}
		defer budget.release(holder, cost)
		limitBodyReadTime(c, readLimits)
		c.Next()
	}
}

type requestBodyBudget struct {
	total          *semaphore.Weighted
	perHolderLimit int64
	mu             sync.Mutex
	held           map[string]int64
}

func newRequestBodyBudget(budgetBytes int64) *requestBodyBudget {
	return &requestBodyBudget{
		total:          semaphore.NewWeighted(budgetBytes),
		perHolderLimit: budgetBytes / 2,
		held:           map[string]int64{},
	}
}

func (b *requestBodyBudget) tryAcquire(holder string, cost int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held[holder]+cost > b.perHolderLimit || !b.total.TryAcquire(cost) {
		return false
	}
	b.held[holder] += cost
	return true
}

func (b *requestBodyBudget) release(holder string, cost int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total.Release(cost)
	if b.held[holder] -= cost; b.held[holder] <= 0 {
		delete(b.held, holder)
	}
}

func requestBodyBudgetHolder(c *gin.Context) string {
	if key := APIKeyFrom(c); key != nil && key.ID != "" {
		return "key:" + key.ID
	}
	if installation := InstallationFrom(c); installation != nil && installation.ID != "" {
		return "installation:" + installation.ID
	}
	return ""
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
	return min(size, requestcontext.MaxRequestBodyBytes) * requestBodyProcessingAmplification
}

func limitBodyReadTime(c *gin.Context, limits RequestBodyReadLimits) {
	controller := http.NewResponseController(c.Writer)
	now := time.Now()
	body := &deadlineClearingBody{
		ReadCloser: c.Request.Body,
		controller: controller,
		idle:       limits.Idle,
		hardStop:   now.Add(limits.Total),
	}
	if controller.SetReadDeadline(body.nextDeadline(now)) != nil {
		return
	}
	body.refreshedAt = now
	c.Request.Body = body
}

type deadlineClearingBody struct {
	io.ReadCloser
	controller  *http.ResponseController
	idle        time.Duration
	hardStop    time.Time
	refreshedAt time.Time
	once        sync.Once
}

func (b *deadlineClearingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		b.clearDeadline()
		return n, err
	}
	if now := time.Now(); n > 0 && now.Sub(b.refreshedAt) >= b.idle/4 {
		b.refreshedAt = now
		_ = b.controller.SetReadDeadline(b.nextDeadline(now))
	}
	return n, err
}

func (b *deadlineClearingBody) nextDeadline(now time.Time) time.Time {
	if b.idle <= 0 {
		return b.hardStop
	}
	idleDeadline := now.Add(b.idle)
	if idleDeadline.Before(b.hardStop) {
		return idleDeadline
	}
	return b.hardStop
}

func (b *deadlineClearingBody) Close() error {
	b.clearDeadline()
	return b.ReadCloser.Close()
}

func (b *deadlineClearingBody) clearDeadline() {
	b.once.Do(func() { _ = b.controller.SetReadDeadline(time.Time{}) })
}
