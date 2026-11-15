package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"

	"github.com/google/uuid"
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
	// Mirrors BuildCredentialsMap: usable or explicitly failed keys only, last one wins.
	keyFirst := false
	for _, key := range externalKeysFromContext(ctx) {
		if key.Provider == provider && (len(key.Plaintext) > 0 || key.CredentialError != nil) {
			keyFirst = key.FundingPriority == auth.FundingPriorityKeyFirst
		}
	}
	return keyFirst
}

func anthropicBYOKFirst(ctx context.Context) bool {
	return byokFirst(ctx, providers.ProviderAnthropic)
}

// BuildCredentialsMap builds provider -> Credentials from external keys.
// Empty-plaintext entries are dropped except explicit provider-scoped failures,
// which prevent fallback to a deployment credential.
func BuildCredentialsMap(keys []*auth.ExternalAPIKey) map[string]*Credentials {
	if len(keys) == 0 {
		return nil
	}
	m := make(map[string]*Credentials, len(keys))
	for _, key := range keys {
		if len(key.Plaintext) == 0 && key.CredentialError == nil {
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
			FundingPriority:        key.FundingPriority,
			CredentialError:        key.CredentialError,
			// The key row, not its plaintext: a WIF or keypair-JWT key's
			// Plaintext is a per-request bearer. AuthAccount/AuthUser are
			// folded in so repointing a key at another upstream account is
			// a different principal.
			PrincipalID:   strings.Join([]string{"external-key", key.ID, key.AuthAccount, key.AuthUser}, ":"),
			ExternalKeyID: key.ID,
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

// resolveAndInjectCredentials resolves credentials for the selected provider
// and model and stashes them on ctx. Claude OAuth applies to Anthropic models;
// Codex OAuth applies to the native Codex roster plus catalog-approved
// subscription-first fallback models. Other selections fall through to BYOK,
// a client API key, or the deployment key.
//
// Without Anthropic BYOK, a caller's own Claude subscription pays for Claude
// turns. Native harnesses leave a sk-ant-oat- bearer in Authorization (or
// x-api-key) while the router key rides in X-Weave-Router-Key. Server-side
// enrollment (`npx @weave-os/router login`) is a separate managed-account path.
//
// The inbound-bearer path is restricted to the OAuth subset: a general client
// API key is NOT extracted on the router-key path, since that would forward
// the client's inbound key to a different upstream provider. The deployment
// env key is the correct fallback there.
func resolveAndInjectCredentials(ctx context.Context, provider, model string, headers http.Header) context.Context {
	byok := BuildCredentialsMap(externalKeysFromContext(ctx))
	if byok[provider] != nil && byok[provider].FundingPriority == auth.FundingPriorityKeyFirst {
		return context.WithValue(ctx, CredentialsContextKey{}, byok[provider])
	}
	routerKeyed := installationIDFromContext(ctx) != (uuid.UUID{})
	// Skip subscription OAuth (fall through to BYOK / deployment key):
	// exhausted (Anthropic-only, avoid re-429), toggle off (provider-wide), or
	// an OpenAI-provider model outside the native Codex roster and catalog
	// exceptions.
	subDisabled := subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx)
	suppressClaudeSub := claudeSubscriptionSuppressed(ctx) || subDisabled || claudeModelSuppressed(ctx, model)
	suppressCodexSub := codexSubscriptionSuppressed(ctx) || codexModelSuppressed(ctx, model) || codexChatEndpoint(ctx) || subDisabled || !codexSubscriptionCanAttemptModel(model)
	if provider == providers.ProviderAnthropic && !suppressClaudeSub {
		if sub := subscriptionCredsFromToken(anthropicSubscriptionFromContext(ctx)); sub != nil {
			return context.WithValue(ctx, CredentialsContextKey{}, sub)
		}
		// A Claude subscription bearer (sk-ant-oat-) in the inbound Authorization
		// is honored even on router-keyed requests: Claude Code keeps its own
		// OAuth token there while the router key rides in X-Weave-Router-Key.
		// Restricted to the OAuth subset — a general API key is still not
		// forwarded on the router-key path (cross-provider-leak guard below).
		if inbound := ExtractClientCredentials(provider, headers); inbound != nil && inbound.OAuth {
			observability.FromContext(ctx).Info("Resolved Claude subscription credential",
				"credential_source", inbound.Source)
			return context.WithValue(ctx, CredentialsContextKey{}, inbound)
		}
	}
	if provider == providers.ProviderOpenAI && !suppressCodexSub {
		if sub := codexSubscriptionFromContext(ctx); sub != nil {
			return context.WithValue(ctx, CredentialsContextKey{}, sub)
		}
		// A Codex subscription bearer (ChatGPT OAuth JWT + ChatGPT-Account-ID) in
		// the inbound Authorization is honored even on router-keyed requests:
		// Codex CLI keeps its ChatGPT auth there while the router key rides in
		// X-Weave-Router-Key. OAuth subset only — a general API key is still not
		// forwarded on the router-key path (cross-provider-leak guard below).
		if inbound := ExtractClientCredentials(provider, headers); inbound != nil && inbound.OAuth {
			observability.FromContext(ctx).Debug("Resolved Codex subscription credential for OpenAI turn", "credential_source", inbound.Source)
			return context.WithValue(ctx, CredentialsContextKey{}, inbound)
		}
	}
	var creds *Credentials
	if byok != nil {
		creds = byok[provider]
	}
	if creds == nil && !routerKeyed {
		client := ExtractClientCredentials(provider, headers)
		// A suppressed subscription must not slip back in as the inbound OAuth
		// bearer off the router-key path, undoing the skip above.
		if client != nil && client.OAuth &&
			((provider == providers.ProviderAnthropic && suppressClaudeSub) ||
				(provider == providers.ProviderOpenAI && suppressCodexSub)) {
			client = nil
		}
		creds = client
	}
	if creds != nil {
		return context.WithValue(ctx, CredentialsContextKey{}, creds)
	}
	// Clear explicitly: router-keyed / no-BYOK ctx still carries the subscription credential from an earlier attempt;
	// provider client only falls back to the deployment key when ctx carries NO credential.
	if (suppressClaudeSub && provider == providers.ProviderAnthropic) ||
		(suppressCodexSub && provider == providers.ProviderOpenAI) {
		return clearCredentials(ctx)
	}
	return ctx
}
