package middleware

import (
	"strings"

	"weave-os/router/internal/refusal"

	"github.com/gin-gonic/gin"
)

func abortRefusal(c *gin.Context, kind refusal.Kind, fields gin.H) {
	contract := kind.Contract()
	setRefusalHeaders(c, contract)
	format, inference := inferenceAPIFormat(c.Request.URL.Path)
	if !inference {
		body := gin.H{"error": contract.Code}
		for key, value := range fields {
			body[key] = value
		}
		c.AbortWithStatusJSON(contract.Status, body)
		return
	}
	message := contract.Message
	if supplied, ok := fields["message"].(string); ok && supplied != "" {
		message = supplied
	}
	inner := gin.H{}
	for key, value := range fields {
		inner[key] = value
	}
	inner["message"] = message
	switch format {
	case apiFormatAnthropic:
		inner["type"] = contract.AnthropicType()
		inner["code"] = contract.Code
		c.AbortWithStatusJSON(contract.Status, gin.H{"type": "error", "error": inner})
	case apiFormatGemini:
		inner["code"] = contract.Status
		inner["status"] = contract.GeminiStatus()
		inner["details"] = []gin.H{{"reason": contract.Code}}
		c.AbortWithStatusJSON(contract.Status, gin.H{"error": inner})
	default:
		inner["type"] = contract.OpenAIType()
		inner["param"] = nil
		inner["code"] = contract.Code
		c.AbortWithStatusJSON(contract.Status, gin.H{"error": inner})
	}
}

func inferenceAPIFormat(path string) (apiFormat, bool) {
	switch {
	case strings.HasPrefix(path, "/v1beta/"):
		return apiFormatGemini, true
	case underPath(path, "/v1/messages"), underPath(path, "/v1/route"):
		return apiFormatAnthropic, true
	case underPath(path, "/v1/chat/completions"), underPath(path, "/v1/responses"):
		return apiFormatOpenAI, true
	default:
		return apiFormatOpenAI, false
	}
}

func underPath(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

func abortBillingUnavailable(c *gin.Context) {
	abortRefusal(c, refusal.BillingUnavailable, gin.H{"message": refusal.BillingUnavailable.Contract().Message})
}

func setRefusalHeaders(c *gin.Context, contract refusal.Contract) {
	if contract.RetryAfter {
		c.Header("Retry-After", "1")
	}
	if contract.NoClientRetry {
		c.Header("x-should-retry", "false")
	}
}
