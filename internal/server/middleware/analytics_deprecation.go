package middleware

import "github.com/gin-gonic/gin"

const publicAnalyticsDeprecatedAt = "@1796083200"

func WithAnalyticsDeprecation() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Deprecation", publicAnalyticsDeprecatedAt)
		c.Next()
	}
}
