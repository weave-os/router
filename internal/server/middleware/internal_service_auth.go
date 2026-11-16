package middleware

import (
	"crypto/subtle"
	"strings"
	"weave-os/router/internal/refusal"

	"github.com/gin-gonic/gin"
)

const internalServiceTokenHeader = "X-Weave-Internal-Token"

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
