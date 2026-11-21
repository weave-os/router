package credentials

import (
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

type Suppression struct {
	Claude bool
	Codex  bool
}

type Resolution struct {
	Credentials         *requestcontext.Credentials
	Clear               bool
	InboundSubscription bool
}

func Resolve(facts requestcontext.RequestFacts, provider string, skip Suppression) Resolution {
	byok := BuildCredentialsMap(facts.ExternalKeys)
	if byok[provider] != nil && byok[provider].FundingPriority == auth.FundingPriorityKeyFirst && !facts.ByokFeeUnpayable {
		return Resolution{Credentials: byok[provider]}
	}
	suppressed := provider == providers.ProviderAnthropic && skip.Claude ||
		provider == providers.ProviderOpenAI && skip.Codex
	if !suppressed {
		var stored *requestcontext.Credentials
		switch provider {
		case providers.ProviderAnthropic:
			stored = requestcontext.SubscriptionCredsFromToken(strings.TrimSpace(facts.AnthropicSubscription))
		case providers.ProviderOpenAI:
			stored = requestcontext.CodexSubscriptionCreds(facts.OpenAISubscription, facts.OpenAIAccountID)
		}
		if stored != nil {
			return Resolution{Credentials: stored}
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

func KeyFirst(keys []*auth.ExternalAPIKey, provider string) bool {
	keyFirst := false
	for _, key := range keys {
		if key.Provider == provider && (len(key.Plaintext) > 0 || key.CredentialError != nil) {
			keyFirst = key.FundingPriority == auth.FundingPriorityKeyFirst
		}
	}
	return keyFirst
}

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
			PrincipalID:            strings.Join([]string{"external-key", key.ID, key.AuthAccount, key.AuthUser}, ":"),
			ExternalKeyID:          key.ID,
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
