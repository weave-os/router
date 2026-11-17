package requestcontext

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const SubscriptionTokenPrefix = "sk-ant-oat"

const ChatGPTAccountIDHeader = "ChatGPT-Account-ID"

const (
	SourceBYOK                = "byok"
	SourceClient              = "client"
	SourceSubscription        = "subscription"
	SourceSubscriptionOverage = "subscription_overage"
	SourceCodexSubscription   = "codex_subscription"
)

type Credentials struct {
	SubscriptionPlan            providers.SubscriptionPlan
	SubscriptionAccountID       string
	SubscriptionQuotaGeneration int64
	APIKey                      []byte
	Source                      string
	OAuth                       bool
	AccountID                   []byte
	BaseURL                     string
	ModelAliases                map[string]string
	IdentityHeader              string
	IdentityHeaderFormat        string
	ForwardedClientHeaders      []string
	BaggageHeader               string
	AuthType                    auth.AuthType
	FundingPriority             auth.FundingPriority
	CredentialError             error
	PrincipalID                 string
	ExternalKeyID               string
}

func (c *Credentials) UpstreamPrincipal() (principal string, secret bool) {
	if c == nil {
		return "", false
	}
	if c.PrincipalID != "" {
		return c.PrincipalID, false
	}
	return string(c.APIKey), true
}

type CredentialsContextKey struct{}

func CredentialsFromContext(ctx context.Context) *Credentials {
	v := ctx.Value(CredentialsContextKey{})
	if v == nil {
		return nil
	}
	creds, _ := v.(*Credentials)
	return creds
}

func WithCredentials(ctx context.Context, creds *Credentials) context.Context {
	return context.WithValue(ctx, CredentialsContextKey{}, creds)
}

func EffectiveBaseURL(ctx context.Context, fallback string) string {
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.BaseURL == "" {
		return fallback
	}
	return strings.TrimRight(creds.BaseURL, "/")
}

func EffectiveUpstreamModel(ctx context.Context, model string) string {
	creds := CredentialsFromContext(ctx)
	if creds == nil {
		return model
	}
	if alias, ok := creds.ModelAliases[model]; ok {
		return alias
	}
	return model
}

func ApplyModelAlias(ctx context.Context, body []byte, model string) []byte {
	creds := CredentialsFromContext(ctx)
	if creds == nil || len(body) == 0 {
		return body
	}
	upstreamModel, aliased := creds.ModelAliases[model]
	if !aliased {
		return body
	}
	if !gjson.GetBytes(body, "model").Exists() {
		return body
	}
	out, err := sjson.SetBytes(body, "model", upstreamModel)
	if err != nil {
		return body
	}
	return out
}

func ExtractClientCredentials(provider string, headers http.Header) *Credentials {
	family := providers.FamilyFor(provider)
	if family == providers.FamilyAnthropic {
		if key := strings.TrimSpace(headers.Get("x-api-key")); key != "" &&
			!auth.HasAPIKeyPrefix(key) && strings.HasPrefix(key, "sk-ant-") {
			return &Credentials{APIKey: []byte(key), Source: SourceClient}
		}
		if raw, found := strings.CutPrefix(headers.Get("Authorization"), "Bearer "); found {
			key := strings.TrimSpace(raw)
			if !auth.HasAPIKeyPrefix(key) {
				if strings.HasPrefix(key, "sk-ant-api-") {
					return &Credentials{APIKey: []byte(key), Source: SourceClient}
				}
				if sub := SubscriptionCredsFromToken(key); sub != nil {
					return sub
				}
			}
		}
		return nil
	}
	if family != providers.FamilyOpenAICompat && family != providers.FamilyGemini {
		return nil
	}
	authHeader := headers.Get("Authorization")
	if raw, found := strings.CutPrefix(authHeader, "Bearer "); found {
		key := strings.TrimSpace(raw)
		if provider == providers.ProviderOpenAI {
			if sub := CodexSubscriptionCreds(key, headers.Get(ChatGPTAccountIDHeader)); sub != nil {
				return sub
			}
		}
		if key != "" && !auth.HasAPIKeyPrefix(key) && !strings.HasPrefix(key, "sk-ant-") {
			return &Credentials{APIKey: []byte(key), Source: SourceClient}
		}
	}
	return nil
}

func SubscriptionCredsFromToken(token string) *Credentials {
	if !strings.HasPrefix(token, SubscriptionTokenPrefix) {
		return nil
	}
	return &Credentials{APIKey: []byte(token), Source: SourceSubscription, OAuth: true}
}

func CodexSubscriptionCreds(token, accountID string) *Credentials {
	token = strings.TrimSpace(token)
	accountID = strings.TrimSpace(accountID)
	if token == "" || accountID == "" {
		return nil
	}
	if auth.HasAPIKeyPrefix(token) || strings.HasPrefix(token, "sk-") {
		return nil
	}
	return &Credentials{
		APIKey:      []byte(token),
		AccountID:   []byte(accountID),
		Source:      SourceCodexSubscription,
		OAuth:       true,
		PrincipalID: "chatgpt-account:" + accountID,
	}
}

func CodexCoveredModels() []string {
	return catalog.CodexSubscriptionModels()
}

func CodexSubscriptionCanAttemptModel(model string) bool {
	if CodexSubscriptionCoversModel(model) {
		return true
	}
	m, ok := catalog.ByID(model)
	if !ok || !m.CodexSubscriptionFallback {
		return false
	}
	for _, binding := range m.Providers {
		if binding.Provider == providers.ProviderOpenAI {
			return true
		}
	}
	return false
}

func CodexSubscriptionCoversModel(model string) bool {
	return catalog.CodexSubscriptionCoversModel(model)
}

func ApplyWIFTokenType(ctx context.Context, upstream *http.Request) {
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.AuthType != auth.AuthTypeWIF {
		return
	}
	upstream.Header.Set(auth.WIFTokenTypeHeader, auth.WIFTokenTypeValue)
}
