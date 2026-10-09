package proxy

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/providers"

	"github.com/tidwall/gjson"
)

// subscriptionThirdPartyDenialTTL keeps a linked Claude account out of one
// client app's leases after Anthropic refused that app on the account's plan
// limits. The refusal lasts until the account holder buys extra usage, so the
// TTL only bounds how often the router re-probes.
const subscriptionThirdPartyDenialTTL = subscriptionModelDenialTTL

// anthropicSubscriptionThirdPartyRefused reports whether err is Anthropic's
// refusal to serve a non-Claude-Code client on a subscription's plan limits
// ("Third-party apps now draw from your extra usage, not your plan limits").
// It arrives as a 400 invalid_request_error, so neither IsRetryable nor the
// OAuth-rejection gate catches it; the account is healthy for Claude Code
// traffic, so it is a per-client-app capacity failure, not a bad credential.
func anthropicSubscriptionThirdPartyRefused(err error) bool {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) || upstream.Status != http.StatusBadRequest {
		return false
	}
	if gjson.GetBytes(upstream.Body, "error.type").String() != "invalid_request_error" {
		return false
	}
	message := strings.ToLower(gjson.GetBytes(upstream.Body, "error.message").String())
	return strings.Contains(message, "third-party apps") && strings.Contains(message, "extra usage")
}

// thirdPartyDenialModel is the model slot a client-app denial occupies in the
// managed denial cache, disjoint from every catalog model ID.
func thirdPartyDenialModel(clientApp string) string {
	return "\x00client-app:" + clientApp
}

func (a *subscriptionModelAccess) denyManagedClientApp(account, provider, clientApp string, until time.Time) {
	a.denyManaged("account:"+account, account, provider, thirdPartyDenialModel(clientApp), until)
}

func (a *subscriptionModelAccess) managedClientAppDenied(account, provider, clientApp string, now time.Time) bool {
	return a.managedDenied("account:"+account, account, provider, thirdPartyDenialModel(clientApp), now)
}
