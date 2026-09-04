package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// subscriptionTokenPrefix marks a Claude subscription (Claude.ai OAuth)
// bearer. Matches the "oat" stem to cover both sk-ant-oat- and the
// real-world sk-ant-oat01-… shapes.
const subscriptionTokenPrefix = "sk-ant-oat"

// chatGPTAccountIDHeader is the header Codex sends alongside a ChatGPT
// subscription bearer; its presence disambiguates that JWT from a plain
// OpenAI API key.
const chatGPTAccountIDHeader = "ChatGPT-Account-ID"

// Credential sources, for logging and precedence reasoning. Never log the key
// itself — only the source.
const (
	credSourceBYOK              = "byok"
	credSourceClient            = "client"
	credSourceSubscription      = "subscription"
	credSourceCodexSubscription = "codex_subscription"
)

// Credentials holds the API key to use for an upstream request.
type Credentials struct {
	APIKey []byte // never logged
	Source string // credSourceBYOK | credSourceClient | credSourceSubscription | credSourceCodexSubscription
	// OAuth marks a subscription bearer (Claude sk-ant-oat- token, Anthropic
	// only; or Codex ChatGPT JWT, OpenAI only). Authenticates via
	// Authorization: Bearer, never x-api-key.
	OAuth bool
	// AccountID is the ChatGPT-Account-ID paired with a Codex subscription
	// bearer; the Codex backend 401/403s without it. Never logged.
	AccountID []byte
	// BaseURL overrides the upstream endpoint per-request; non-empty only on BYOK
	// credentials, where the boot-time deployment URL is provider-wide, not per-key.
	BaseURL string
	// ModelAliases rewrites the outbound model ID for endpoints publishing the
	// catalog's models under their own names; non-empty only on BYOK credentials.
	ModelAliases map[string]string
	// IdentityHeader and IdentityHeaderFormat name and shape the header this
	// endpoint wants the caller's identity in; empty forwards nothing.
	IdentityHeader       string
	IdentityHeaderFormat string
	// ForwardedClientHeaders and BaggageHeader configure client-header passthrough; empty forwards nothing.
	ForwardedClientHeaders []string
	BaggageHeader          string
	// AuthType is the BYOK key's auth mode (see auth.AuthType*). APIKey already holds
	// the derived credential; this tells the adapter how the upstream must read it.
	AuthType string
}

// EffectiveBaseURL returns the BYOK key's per-request base URL if set,
// or fallback (the provider's boot-time deployment URL).
func EffectiveBaseURL(ctx context.Context, fallback string) string {
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.BaseURL == "" {
		return fallback
	}
	return strings.TrimRight(creds.BaseURL, "/")
}

// EffectiveUpstreamModel returns the aliased model name for this request's endpoint, or model
// unchanged. Only the outbound wire name changes -- routing, pricing, and telemetry use the catalog ID.
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

// ApplyModelAlias rewrites the body's top-level "model" field via the BYOK credential alias.
// Unaliased bodies are returned untouched; the envelope stays the authority on every other request.
func ApplyModelAlias(ctx context.Context, body []byte, model string) []byte {
	creds := CredentialsFromContext(ctx)
	if creds == nil || len(body) == 0 {
		return body
	}
	// Presence, not inequality: an alias equal to the catalog id still has to
	// overwrite a catalog UpstreamID an adapter already wrote into the body.
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
			ForwardedClientHeaders: key.ForwardedClientHeaders,
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
// headers. Anthropic uses x-api-key; OpenAI and Google use
// Authorization: Bearer.
//
// Rejects any token with auth.APIKeyPrefix — router-issued bearers (rk_...)
// use the same headers via WithAuth, and this stops them leaking upstream.
func ExtractClientCredentials(provider string, headers http.Header) *Credentials {
	// Anthropic reads x-api-key / sk-ant- shapes and keeps its own branch.
	// Every other family shares the Authorization: Bearer branch, keyed off
	// family so a new OpenAI-compat provider works without editing a list.
	family := providers.FamilyFor(provider)
	if family == providers.FamilyAnthropic {
		// Requiring the sk-ant- prefix here prevents a misplaced
		// cross-provider key (e.g. an OpenAI key in x-api-key) from being
		// misclassified as Anthropic creds.
		if key := strings.TrimSpace(headers.Get("x-api-key")); key != "" &&
			!auth.HasAPIKeyPrefix(key) && strings.HasPrefix(key, "sk-ant-") {
			return &Credentials{APIKey: []byte(key), Source: credSourceClient}
		}
		// sk-ant-api-… is a legitimate client key. sk-ant-oat-… is a Claude
		// subscription (Claude.ai login) token — works against /v1/messages
		// only via Bearer + oauth beta header, no x-api-key. Forwarded so
		// the caller's subscription pays for their turns.
		if raw, found := strings.CutPrefix(headers.Get("Authorization"), "Bearer "); found {
			key := strings.TrimSpace(raw)
			if !auth.HasAPIKeyPrefix(key) {
				if strings.HasPrefix(key, "sk-ant-api-") {
					return &Credentials{APIKey: []byte(key), Source: credSourceClient}
				}
				if sub := subscriptionCredsFromToken(key); sub != nil {
					return sub
				}
			}
		}
		return nil
	}
	// OpenAI-compat upstreams and Gemini/Google authenticate via
	// Authorization: Bearer. FamilyUnknown providers fall through to nil.
	if family != providers.FamilyOpenAICompat && family != providers.FamilyGemini {
		return nil
	}
	authHeader := headers.Get("Authorization")
	if raw, found := strings.CutPrefix(authHeader, "Bearer "); found {
		key := strings.TrimSpace(raw)
		// A Codex ChatGPT subscription bearer pairs with a ChatGPT-Account-ID
		// header; resolve it before the client-key branch. OpenAI-only, so a
		// stray header on another route can't reclassify its bearer.
		if provider == providers.ProviderOpenAI {
			if sub := codexSubscriptionCreds(key, headers.Get(chatGPTAccountIDHeader)); sub != nil {
				return sub
			}
		}
		// Reject Anthropic-shaped tokens (keys and OAuth bearers) so one
		// Bearer header isn't misidentified as creds for every provider.
		if key != "" && !auth.HasAPIKeyPrefix(key) && !strings.HasPrefix(key, "sk-ant-") {
			return &Credentials{APIKey: []byte(key), Source: credSourceClient}
		}
	}
	return nil
}

// subscriptionCredsFromToken returns subscription credentials for a bare
// token, or nil if it isn't a Claude subscription bearer. Anthropic-only.
func subscriptionCredsFromToken(token string) *Credentials {
	if !strings.HasPrefix(token, subscriptionTokenPrefix) {
		return nil
	}
	return &Credentials{APIKey: []byte(token), Source: credSourceSubscription, OAuth: true}
}

// codexSubscriptionCreds returns Codex subscription credentials for a
// ChatGPT-login JWT paired with its account id, or nil otherwise. Rejects
// router keys and OpenAI API keys; requires a non-empty account id since
// the Codex backend 401/403s without it. OpenAI-only.
func codexSubscriptionCreds(token, accountID string) *Credentials {
	token = strings.TrimSpace(token)
	accountID = strings.TrimSpace(accountID)
	if token == "" || accountID == "" {
		return nil
	}
	if auth.HasAPIKeyPrefix(token) || strings.HasPrefix(token, "sk-") {
		return nil
	}
	return &Credentials{
		APIKey:    []byte(token),
		AccountID: []byte(accountID),
		Source:    credSourceCodexSubscription,
		OAuth:     true,
	}
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
