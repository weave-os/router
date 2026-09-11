package middleware

import (
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"

	"github.com/gin-gonic/gin"
)

// PolicyPinOverrideHeader pins a turn to one frozen policy artifact and roster:
// `<policy_artifact_sha256>@<roster_sha256>`. Only registered when
// ROUTER_POLICY_PIN_ENABLED is set; honoured only for installations with
// PolicyHeaderOverridesEnabled. For any other installation the value is never
// parsed: the request is served exactly as if the header were absent, apart
// from a policy_pin_requested telemetry mark. A malformed value from an
// authorized installation is a 400 with code policy_pin_malformed.
const PolicyPinOverrideHeader = "x-weave-policy-pin"

// WithPolicyPinOverride parses the pin header onto the request context so the
// router can select (or refuse) the pinned artifact and roster.
func WithPolicyPinOverride() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader(PolicyPinOverrideHeader)
		if raw == "" {
			c.Next()
			return
		}
		installation := InstallationFrom(c)
		if installation == nil || !installation.PolicyHeaderOverridesEnabled {
			observability.FromGin(c).Debug("Policy pin ignored: installation is not authorized for policy headers")
			ctx := router.WithPolicyPinRequest(c.Request.Context(), router.PolicyPinRequest{})
			c.Request = c.Request.WithContext(ctx)
			c.Next()
			return
		}
		pin, err := router.ParsePolicyPin(raw)
		if err != nil {
			abortMalformedPolicyPin(c, err.Error())
			return
		}
		observability.FromGin(c).Info("Policy pin applied", "installation_id", installation.ID, "policy_pin", pin.String())
		ctx := router.WithPolicyPinRequest(c.Request.Context(), router.PolicyPinRequest{Pin: pin, Authorized: true})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func abortMalformedPolicyPin(c *gin.Context, message string) {
	code := string(router.PolicyPinErrorMalformed)
	switch detectAPIFormat(c.Request.URL.Path) {
	case apiFormatAnthropic:
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "invalid_request_error",
				"code":    code,
				"message": message,
			},
		})
	case apiFormatGemini:
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    http.StatusBadRequest,
				"message": message,
				"status":  "INVALID_ARGUMENT",
				"details": []gin.H{{"reason": code}},
			},
		})
	default:
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": message,
				"param":   nil,
				"code":    code,
			},
		})
	}
}
