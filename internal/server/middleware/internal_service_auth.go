package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// internalServiceTokenHeader carries the shared secret the Weave control plane
// authenticates with. It is deliberately not Authorization: nothing on this
// surface accepts an installation key, so a leaked rk_ token cannot reach it
// by being sent the usual way.
const internalServiceTokenHeader = "X-Weave-Internal-Token" //nolint:gosec // header name, not a credential

// WithInternalServiceAuth authenticates control-plane-to-router calls against
// a shared secret. The control plane owns installation ownership checks — it
// reaches this surface only for work that needs credentials the router mints
// per request and never hands out, so requests name the installation they act
// for rather than proving a session.
//
// Routes using it must not be mounted when token is empty.
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
