package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

const subscriptionThirdPartyDenialTTL = subscriptionModelDenialTTL

var ErrSubscriptionClientAppRefused = errors.New("claude subscription refused third-party client")

func markSubscriptionClientAppRefused(ctx context.Context) {
	if usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
		usage.ClientAppRefused = true
	}
}

type APIKeyPaidFallbackOnSubscriptionRefusalContextKey struct{}

type clientAppRefusalPaidFallbackKey struct{}

func withClientAppRefusalPaidFallback(ctx context.Context, decision router.Decision) context.Context {
	consented, _ := ctx.Value(APIKeyPaidFallbackOnSubscriptionRefusalContextKey{}).(bool)
	forcedConsent := consented && strings.HasPrefix(decision.Reason, translate.ReasonUserForceModel)
	if !forcedConsent && weaveCreditsDenied(ctx) {
		return ctx
	}
	return context.WithValue(ctx, clientAppRefusalPaidFallbackKey{}, forcedConsent)
}

func clientAppRefusalPaidFallback(ctx context.Context) bool {
	_, allowed := ctx.Value(clientAppRefusalPaidFallbackKey{}).(bool)
	return allowed
}

func claudePoolRefusesClientApp(ctx context.Context) bool {
	if spendsPool, _ := ctx.Value(clientAppRefusalPaidFallbackKey{}).(bool); !spendsPool {
		return false
	}
	usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return usage != nil && usage.ClientAppRefused
}

func forcedClaudeClientAppRefused(ctx context.Context, decision router.Decision, creds *Credentials) bool {
	if creds != nil && (creds.OAuth || creds.Source == credSourceBYOK) || clientAppRefusalPaidFallback(ctx) {
		return false
	}
	usage, _ := ctx.Value(credentials.ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return usage != nil && usage.ClientAppRefused && strings.HasPrefix(decision.Reason, translate.ReasonUserForceModel)
}

func anthropicInvalidRequestMessage(err error) string {
	var upstream *providers.UpstreamErrorResponse
	if !errors.As(err, &upstream) || upstream.Status != http.StatusBadRequest {
		return ""
	}
	if gjson.GetBytes(upstream.Body, "error.type").String() != "invalid_request_error" {
		return ""
	}
	return strings.ToLower(gjson.GetBytes(upstream.Body, "error.message").String())
}

func anthropicSubscriptionThirdPartyRefused(err error) bool {
	message := anthropicInvalidRequestMessage(err)
	return strings.Contains(message, "third-party apps") && strings.Contains(message, "extra usage")
}

func anthropicClientAppRefused(ctx context.Context, err error) bool {
	if anthropicSubscriptionThirdPartyRefused(err) {
		return true
	}
	return knownThirdPartyClientApp(ClientIdentityFrom(ctx).ClientApp) && strings.Contains(anthropicInvalidRequestMessage(err), "extra usage")
}

func knownThirdPartyClientApp(clientApp string) bool {
	switch clientApp {
	case ClientAppCodex, ClientAppCursor, ClientAppGeminiCLI, ClientAppOpencode:
		return true
	}
	return false
}

func thirdPartyDenialModel(clientApp string) string {
	return "\x00client-app:" + clientApp
}

func (a *subscriptionModelAccess) denyManagedClientApp(account, provider, clientApp string, until time.Time) {
	a.denyManaged("account:"+account, account, provider, thirdPartyDenialModel(clientApp), until)
}

func (a *subscriptionModelAccess) managedClientAppDenied(account, provider, clientApp string, now time.Time) bool {
	return a.managedDenied("account:"+account, account, provider, thirdPartyDenialModel(clientApp), now)
}
