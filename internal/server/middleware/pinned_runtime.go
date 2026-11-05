package middleware

import (
	"context"

	"github.com/gin-gonic/gin"
	"weave-os/router/internal/policyregistry"
)

type pinnedRuntimeContextKey struct{}

// WithPinnedRuntime binds local requests without minting managed serving identities.
// This middleware is configured by the composition root, never by request headers.
func WithPinnedRuntime(runtime *policyregistry.PinnedRuntime) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := context.WithValue(runtime.Bind(c.Request.Context()), pinnedRuntimeContextKey{}, true)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
