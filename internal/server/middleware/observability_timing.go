package middleware

import (
	"weave-os/router/internal/timing"

	"github.com/gin-gonic/gin"
)

func WithTimingEntry() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, t := timing.WithTiming(c.Request.Context())
		t.StampEntry()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
