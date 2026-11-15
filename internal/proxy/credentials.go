package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

// Credential sources, for logging and precedence reasoning. Never log the key
// itself — only the source.
const (
	credSourceBYOK                = requestcontext.SourceBYOK
	credSourceClient              = requestcontext.SourceClient
	credSourceSubscription        = requestcontext.SourceSubscription
	credSourceSubscriptionOverage = requestcontext.SourceSubscriptionOverage
	credSourceCodexSubscription   = requestcontext.SourceCodexSubscription
)

// Credentials is the request-scoped upstream credential; defined in
// internal/requestcontext so provider adapters can read it without importing
// this package.
type Credentials = requestcontext.Credentials

// CredentialsContextKey is the request-context key for resolved per-request credentials.
type CredentialsContextKey = requestcontext.CredentialsContextKey

// CredentialsFromContext returns the resolved credentials stashed on ctx.
// An explicit nil override from clearCredentials keeps synthetic calls off the
// winning credential.
func CredentialsFromContext(ctx context.Context) *Credentials {
	if override, set := ctx.Value(CredentialsContextKey{}).(*Credentials); set && override == nil {
		return nil
	}
	if usage, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage); usage != nil && usage.Finished {
		return usage.WinningCredentials
	}
	return requestcontext.CredentialsFromContext(ctx)
}

// EffectiveBaseURL returns the BYOK key's per-request base URL if set,
// or fallback (the provider's boot-time deployment URL).
func EffectiveBaseURL(ctx context.Context, fallback string) string {
	return requestcontext.EffectiveBaseURL(ctx, fallback)
}

// EffectiveUpstreamModel returns the aliased model name for this request's endpoint, or model
// unchanged.
func EffectiveUpstreamModel(ctx context.Context, model string) string {
	return requestcontext.EffectiveUpstreamModel(ctx, model)
}

// ApplyModelAlias rewrites the body's top-level "model" field via the BYOK credential alias.
func ApplyModelAlias(ctx context.Context, body []byte, model string) []byte {
	return requestcontext.ApplyModelAlias(ctx, body, model)
}

// ApplyWIFTokenType marks the bearer as a workload attestation, not an upstream-issued token.
func ApplyWIFTokenType(ctx context.Context, upstream *http.Request) {
	requestcontext.ApplyWIFTokenType(ctx, upstream)
}

// ApplyIdentityHeader sets the caller-identity header the BYOK endpoint configured.
func ApplyIdentityHeader(ctx context.Context, upstream *http.Request) {
	requestcontext.ApplyIdentityHeader(ctx, upstream)
}

// ExternalAPIKeysContextKey is the request-context key for external API keys
// stashed by the auth middleware.
type ExternalAPIKeysContextKey struct{}

// byokFirst reports whether the provider's BYOK key is set to be spent before
// any subscription. Such a key is an explicit funding preference: probing
// Claude OAuth first can already incur Enterprise overage before its response
// reveals the charge. A plan-first key leaves subscriptions in play and only
// funds turns no subscription can serve.
func byokFirst(ctx context.Context, provider string) bool {
	return credentials.KeyFirst(externalKeysFromContext(ctx), provider)
}

func anthropicBYOKFirst(ctx context.Context) bool {
	return byokFirst(ctx, providers.ProviderAnthropic)
}

// BuildCredentialsMap builds provider -> Credentials from external keys. See
// credentials.BuildCredentialsMap.
func BuildCredentialsMap(keys []*auth.ExternalAPIKey) map[string]*Credentials {
	return credentials.BuildCredentialsMap(keys)
}

// ExtractClientCredentials extracts provider credentials from request
// headers. See requestcontext.ExtractClientCredentials.
func ExtractClientCredentials(provider string, headers http.Header) *Credentials {
	return requestcontext.ExtractClientCredentials(provider, headers)
}

func subscriptionCredsFromToken(token string) *Credentials {
	return requestcontext.SubscriptionCredsFromToken(strings.TrimSpace(token))
}

func codexSubscriptionCreds(token, accountID string) *Credentials {
	return requestcontext.CodexSubscriptionCreds(token, accountID)
}

// clearCredentials sets an explicit nil so CredentialsFromContext reports
// none and the provider client falls back to its deployment key. Used to
// keep a caller's subscription off synthetic upstream calls (e.g. the
// handover summarizer), which lack the identity a subscription token needs.
func clearCredentials(ctx context.Context) context.Context {
	return context.WithValue(ctx, CredentialsContextKey{}, (*Credentials)(nil))
}

// resolveAndInjectCredentials stashes credentials.Resolve's pick for the
// selected provider and model on ctx, where provider adapters read it. Claude
// OAuth applies to Anthropic models; Codex OAuth applies to the native Codex
// roster plus catalog-approved subscription-first fallback models.
func resolveAndInjectCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	res := credentials.Resolve(requestFactsFrom(ctx, headers), provider, subscriptionSuppression(ctx, model))
	if res.Credentials == nil {
		if res.Clear {
			// The ctx may still carry an earlier attempt's subscription; the
			// provider client only uses its deployment key when ctx has none.
			return clearCredentials(ctx)
		}
		return ctx
	}
	if res.InboundSubscription {
		log := observability.FromContext(ctx)
		if provider == providers.ProviderAnthropic {
			log.Info("Resolved Claude subscription credential", "credential_source", res.Credentials.Source)
		} else {
			log.Debug("Resolved Codex subscription credential for OpenAI turn", "credential_source", res.Credentials.Source)
		}
	}
	return context.WithValue(ctx, CredentialsContextKey{}, res.Credentials)
}

// subscriptionSuppression skips subscription OAuth when it is observed
// exhausted or denied for model, turned off for the request, or (Codex) the
// model is outside the native roster and catalog exceptions.
func subscriptionSuppression(ctx context.Context, model string) credentials.Suppression {
	disabled := subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx)
	return credentials.Suppression{
		Claude: claudeSubscriptionSuppressed(ctx) || disabled || claudeModelSuppressed(ctx, model),
		Codex:  codexSubscriptionSuppressed(ctx) || codexModelSuppressed(ctx, model) || codexChatEndpoint(ctx) || disabled || !codexSubscriptionCanAttemptModel(model),
	}
}
