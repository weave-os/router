package gemini

import (
	"testing"

	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
)

// TestRefusalContract_GeminiStatus pins the Gemini "status" string each
// dispatch refusal answers with.
func TestRefusalContract_GeminiStatus(t *testing.T) {
	for kind, want := range map[proxy.DispatchErrorKind]string{
		proxy.DispatchErrorProductIneligible:            "UPSTREAM_ERROR",
		proxy.DispatchErrorSubscriptionClientAppRefused: "UPSTREAM_ERROR",
		proxy.DispatchErrorSpendCapReached:              "UPSTREAM_ERROR",
		proxy.DispatchErrorCreditsExhausted:             "UPSTREAM_ERROR",
		proxy.DispatchErrorWeaveCreditsDisabled:         "UPSTREAM_ERROR",
		proxy.DispatchErrorClaudeEnterpriseOnly:         "UPSTREAM_ERROR",
		proxy.DispatchErrorSubscriptionPoolExhausted:    "UPSTREAM_ERROR",
		proxy.DispatchErrorSubscriptionPoolThrottled:    "UPSTREAM_ERROR",
		proxy.DispatchErrorSubscriptionPoolUnavailable:  "UPSTREAM_ERROR",
		proxy.DispatchErrorUserSpendLimitReached:        "UPSTREAM_ERROR",
		proxy.DispatchErrorSpendLimitUnavailable:        "UPSTREAM_ERROR",
		proxy.DispatchErrorPolicyPinUnavailable:         "UNAVAILABLE",
	} {
		assert.Equal(t, want, geminiErrorStatus(kind), "kind %d", kind)
	}
}
