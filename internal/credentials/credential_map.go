package credentials

import (
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

func BuildCredentialsMap(keys []*auth.ExternalAPIKey) map[string]*requestcontext.Credentials {
	m := make(map[string]*requestcontext.Credentials, len(keys))
	for _, key := range keys {
		if len(key.Plaintext) == 0 && key.CredentialError == nil {
			continue
		}
		m[key.Provider] = &requestcontext.Credentials{
			APIKey:       key.Plaintext,
			Source:       requestcontext.SourceBYOK,
			BaseURL:      key.BaseURL,
			ModelAliases: wireModelAliases(key.ModelAliases),

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

func wireModelAliases(aliases map[string]string) map[string]string {
	if aliases == nil {
		return nil
	}
	out := make(map[string]string, len(aliases))
	for model, alias := range aliases {
		if !router.IsRoutingModel(alias) {
			out[model] = alias
		}
	}
	return out
}
