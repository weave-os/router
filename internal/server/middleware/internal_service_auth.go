package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const internalServiceTokenHeader = "X-Weave-Internal-Token"

func WithInternalServiceAuth(token string) gin.HandlerFunc {
	expected := []byte(token)
	return func(c *gin.Context) {
		presented := strings.TrimSpace(c.GetHeader(internalServiceTokenHeader))
		if len(expected) == 0 || subtle.ConstantTimeCompare([]byte(presented), expected) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}
		c.Next()
	}
}
