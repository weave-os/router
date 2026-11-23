package credentials

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

const SubscriptionThirdPartyDenialTTL = SubscriptionModelDenialTTL

var ErrSubscriptionClientAppRefused = errors.New("claude subscription refused third-party client")

func MarkSubscriptionClientAppRefused(ctx context.Context) {
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil {
		usage.ClientAppRefused = true
	}
}

type clientAppRefusalPaidFallbackKey struct{}

func WithClientAppRefusalPaidFallback(ctx context.Context, decision router.Decision) context.Context {
	consented, _ := ctx.Value(requestcontext.APIKeyPaidFallbackOnSubscriptionRefusalContextKey{}).(bool)
	forcedConsent := consented && strings.HasPrefix(decision.Reason, translate.ReasonUserForceModel)
	if !forcedConsent && billing.WeaveCreditsDeniedFromContext(ctx) {
		return ctx
	}
	return context.WithValue(ctx, clientAppRefusalPaidFallbackKey{}, forcedConsent)
}

func ClientAppRefusalPaidFallback(ctx context.Context) bool {
	_, allowed := ctx.Value(clientAppRefusalPaidFallbackKey{}).(bool)
	return allowed
}

func ClaudePoolRefusesClientApp(ctx context.Context) bool {
	if spendsPool, _ := ctx.Value(clientAppRefusalPaidFallbackKey{}).(bool); !spendsPool {
		return false
	}
	usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
	return usage != nil && usage.ClientAppRefused
}

func ForcedClaudeClientAppRefused(ctx context.Context, decision router.Decision, creds *requestcontext.Credentials) bool {
	if creds != nil && (creds.OAuth || creds.Source == requestcontext.SourceBYOK) || ClientAppRefusalPaidFallback(ctx) {
		return false
	}
	usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
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

func AnthropicSubscriptionThirdPartyRefused(err error) bool {
	message := anthropicInvalidRequestMessage(err)
	return strings.Contains(message, "third-party apps") && strings.Contains(message, "extra usage")
}

func AnthropicClientAppRefused(ctx context.Context, err error) bool {
	if AnthropicSubscriptionThirdPartyRefused(err) {
		return true
	}
	return knownThirdPartyClientApp(requestcontext.ClientIdentityFrom(ctx).ClientApp) && strings.Contains(anthropicInvalidRequestMessage(err), "extra usage")
}

func knownThirdPartyClientApp(clientApp string) bool {
	switch clientApp {
	case requestcontext.ClientAppCodex, requestcontext.ClientAppCursor, requestcontext.ClientAppGeminiCLI, requestcontext.ClientAppOpencode:
		return true
	}
	return false
}
