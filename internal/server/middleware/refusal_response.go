package middleware

import (
	"weave-os/router/internal/refusal"

	"github.com/gin-gonic/gin"
)

const billingUnavailableMessage = "Billing system is temporarily unavailable. Retry in a few moments."

// abortRefusal answers a middleware refusal with the kind's contract: a flat
// {"error": code, ...fields} body and the contract's retry headers.
func abortRefusal(c *gin.Context, kind refusal.Kind, fields gin.H) {
	contract := kind.Contract()
	setRefusalHeaders(c, contract)
	body := gin.H{"error": contract.Code}
	for key, value := range fields {
		body[key] = value
	}
	c.AbortWithStatusJSON(contract.Status, body)
}

func abortBillingUnavailable(c *gin.Context) {
	abortRefusal(c, refusal.BillingUnavailable, gin.H{"message": billingUnavailableMessage})
}

func setRefusalHeaders(c *gin.Context, contract refusal.Contract) {
	if contract.RetryAfter {
		c.Header("Retry-After", "1")
	}
	if contract.NoClientRetry {
		c.Header("x-should-retry", "false")
	}
}
