package credentials

import (
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

// Suppression is the subscription credentials the orchestrator keeps off one
// resolution (observed exhaustion, a model denial, the installation toggle),
// so the turn falls through to BYOK, a client key or the deployment key.
type Suppression struct {
	Claude bool
	Codex  bool
}

// Resolution is the credential one attempt spends. A zero Resolution leaves
// the request's credential as it is.
type Resolution struct {
	Credentials *requestcontext.Credentials
	// Clear drops a credential an earlier attempt resolved, so the provider
	// client falls back to its deployment key instead of the suppressed one.
	Clear bool
	// InboundSubscription marks Credentials as the caller's own subscription
	// bearer read from the inbound headers.
	InboundSubscription bool
}

// Resolve picks the credential for provider. A key_first BYOK key wins; then
// the caller's Claude or Codex subscription unless suppressed; then any BYOK
// key; then, off the router-key path only, a client key from the headers.
//
// A caller's subscription bearer is honored even on router-keyed requests,
// since native harnesses keep it in Authorization while the router key rides
// in X-Weave-Router-Key. A general client API key never is: that would forward
// the caller's inbound key to another upstream.
func Resolve(facts requestcontext.RequestFacts, provider string, skip Suppression) Resolution {
	byok := BuildCredentialsMap(facts.ExternalKeys)
	if byok[provider] != nil && byok[provider].FundingPriority == auth.FundingPriorityKeyFirst {
		return Resolution{Credentials: byok[provider]}
	}
	suppressed := provider == providers.ProviderAnthropic && skip.Claude ||
		provider == providers.ProviderOpenAI && skip.Codex
	if !suppressed {
		if sub := storedSubscription(facts, provider); sub != nil {
			return Resolution{Credentials: sub}
		}
		if provider == providers.ProviderAnthropic || provider == providers.ProviderOpenAI {
			if inbound := requestcontext.ExtractClientCredentials(provider, facts.Headers); inbound != nil && inbound.OAuth {
				return Resolution{Credentials: inbound, InboundSubscription: true}
			}
		}
	}
	creds := byok[provider]
	if creds == nil && !facts.RouterKeyed() {
		client := requestcontext.ExtractClientCredentials(provider, facts.Headers)
		// A suppressed subscription must not slip back in as the inbound OAuth
		// bearer off the router-key path.
		if client != nil && client.OAuth && suppressed {
			client = nil
		}
		creds = client
	}
	if creds != nil {
		return Resolution{Credentials: creds}
	}
	return Resolution{Clear: suppressed}
}

// storedSubscription is the subscription an internal caller already
// authenticated for provider. A key_first BYOK key for provider returned
// before this is reached, so funding priority needs no check here.
func storedSubscription(facts requestcontext.RequestFacts, provider string) *requestcontext.Credentials {
	switch provider {
	case providers.ProviderAnthropic:
		return requestcontext.SubscriptionCredsFromToken(strings.TrimSpace(facts.AnthropicSubscription))
	case providers.ProviderOpenAI:
		return requestcontext.CodexSubscriptionCreds(facts.OpenAISubscription, facts.OpenAIAccountID)
	}
	return nil
}

// KeyFirst reports whether provider's BYOK key is set to be spent before any
// subscription. It mirrors BuildCredentialsMap: usable or explicitly failed
// keys only, last one wins.
func KeyFirst(keys []*auth.ExternalAPIKey, provider string) bool {
	keyFirst := false
	for _, key := range keys {
		if key.Provider == provider && (len(key.Plaintext) > 0 || key.CredentialError != nil) {
			keyFirst = key.FundingPriority == auth.FundingPriorityKeyFirst
		}
	}
	return keyFirst
}

// BuildCredentialsMap builds provider -> Credentials from external keys.
// Empty-plaintext entries are dropped except explicit provider-scoped failures,
// which prevent fallback to a deployment credential.
func BuildCredentialsMap(keys []*auth.ExternalAPIKey) map[string]*requestcontext.Credentials {
	if len(keys) == 0 {
		return nil
	}
	m := make(map[string]*requestcontext.Credentials, len(keys))
	for _, key := range keys {
		if len(key.Plaintext) == 0 && key.CredentialError == nil {
			continue
		}
		m[key.Provider] = &requestcontext.Credentials{
			APIKey:       key.Plaintext,
			Source:       requestcontext.SourceBYOK,
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
