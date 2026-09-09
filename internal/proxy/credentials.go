package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
)

// Credential sources, for logging and precedence reasoning. Never log the key
// itself — only the source.
const (
	credSourceBYOK              = requestcontext.SourceBYOK
	credSourceClient            = requestcontext.SourceClient
	credSourceSubscription      = requestcontext.SourceSubscription
	credSourceCodexSubscription = requestcontext.SourceCodexSubscription
)

// Credentials is the request-scoped upstream credential; defined in
// internal/requestcontext so provider adapters can read it without importing
// this package.
type Credentials = requestcontext.Credentials

// CredentialsContextKey is the request-context key for resolved per-request credentials.
type CredentialsContextKey = requestcontext.CredentialsContextKey

// CredentialsFromContext returns the resolved credentials stashed on ctx.
func CredentialsFromContext(ctx context.Context) *Credentials {
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

// BuildCredentialsMap builds provider -> Credentials from external keys.
// Empty-plaintext entries are dropped so the scorer doesn't route to a
// provider whose upstream call would 401.
func BuildCredentialsMap(keys []*auth.ExternalAPIKey) map[string]*Credentials {
	if len(keys) == 0 {
		return nil
	}
	m := make(map[string]*Credentials, len(keys))
	for _, key := range keys {
		if len(key.Plaintext) == 0 {
			continue
		}
		m[key.Provider] = &Credentials{
			APIKey:       key.Plaintext,
			Source:       credSourceBYOK,
			BaseURL:      key.BaseURL,
			ModelAliases: key.ModelAliases,

			IdentityHeader:         key.IdentityHeader,
			IdentityHeaderFormat:   key.IdentityHeaderFormat,
			ForwardedClientHeaders: append([]string(nil), key.ForwardedClientHeaders...),
			BaggageHeader:          key.BaggageHeader,
			AuthType:               key.AuthType,
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// ExtractClientCredentials extracts provider credentials from request
// headers. See requestcontext.ExtractClientCredentials.
func ExtractClientCredentials(provider string, headers http.Header) *Credentials {
	return requestcontext.ExtractClientCredentials(provider, headers)
}

func subscriptionCredsFromToken(token string) *Credentials {
	return requestcontext.SubscriptionCredsFromToken(token)
}

func codexSubscriptionCreds(token, accountID string) *Credentials {
	return requestcontext.CodexSubscriptionCreds(token, accountID)
}

// subscriptionCredsFromHeaderValue resolves the X-Weave-Anthropic-Subscription
// header into subscription credentials, or nil if empty/router-keyed/invalid.
func subscriptionCredsFromHeaderValue(sub string) *Credentials {
	sub = strings.TrimSpace(sub)
	if sub == "" || auth.HasAPIKeyPrefix(sub) {
		return nil
	}
	return subscriptionCredsFromToken(sub)
}

// clearCredentials sets an explicit nil so CredentialsFromContext reports
// none and the provider client falls back to its deployment key. Used to
// keep a caller's subscription off synthetic upstream calls (e.g. the
// handover summarizer), which lack the identity a subscription token needs.
func clearCredentials(ctx context.Context) context.Context {
	return context.WithValue(ctx, CredentialsContextKey{}, (*Credentials)(nil))
}
