package middleware

import (
	"crypto/subtle"
	"strings"
	"weave-os/router/internal/refusal"

	"github.com/gin-gonic/gin"
)

// internalServiceTokenHeader is deliberately not Authorization so a leaked
// rk_ key cannot reach this surface by being sent the usual way.
const internalServiceTokenHeader = "X-Weave-Internal-Token"

// WithInternalServiceAuth authenticates control-plane-to-router calls against
// a shared secret. Rejects everything when token is empty.
func WithInternalServiceAuth(token string) gin.HandlerFunc {
	expected := []byte(token)
	return func(c *gin.Context) {
		presented := strings.TrimSpace(c.GetHeader(internalServiceTokenHeader))
		if len(expected) == 0 || subtle.ConstantTimeCompare([]byte(presented), expected) != 1 {
			abortRefusal(c, refusal.InvalidKey, nil)
			return
		}
		c.Next()
	}
}
