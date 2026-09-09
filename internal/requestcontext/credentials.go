// Package requestcontext carries the request-scoped, provider-neutral values a
// provider adapter needs to shape one upstream call: resolved credentials,
// effective base URL and model alias, caller identity, and forwarded
// correlation headers. It is inner-ring and I/O-free so adapters can depend on
// it without importing the dispatch orchestrator.
package requestcontext

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SubscriptionTokenPrefix marks a Claude subscription (Claude.ai OAuth)
// bearer. Matches the "oat" stem to cover both sk-ant-oat- and the
// real-world sk-ant-oat01-… shapes.
const SubscriptionTokenPrefix = "sk-ant-oat"

// ChatGPTAccountIDHeader is the header Codex sends alongside a ChatGPT
// subscription bearer; its presence disambiguates that JWT from a plain
// OpenAI API key.
const ChatGPTAccountIDHeader = "ChatGPT-Account-ID"

// Credential sources, for logging and precedence reasoning. Never log the key
// itself — only the source.
const (
	SourceBYOK              = "byok"
	SourceClient            = "client"
	SourceSubscription      = "subscription"
	SourceCodexSubscription = "codex_subscription"
)

// Credentials holds the API key to use for an upstream request.
type Credentials struct {
	APIKey []byte // never logged
	Source string // SourceBYOK | SourceClient | SourceSubscription | SourceCodexSubscription
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

// CredentialsContextKey is the request-context key for resolved per-request credentials.
type CredentialsContextKey struct{}

// CredentialsFromContext returns the resolved credentials stashed on ctx.
func CredentialsFromContext(ctx context.Context) *Credentials {
	v := ctx.Value(CredentialsContextKey{})
	if v == nil {
		return nil
	}
	creds, _ := v.(*Credentials)
	return creds
}

// WithCredentials stashes creds on ctx. A nil creds is an explicit "none" so
// CredentialsFromContext reports nil and the adapter uses its deployment key.
func WithCredentials(ctx context.Context, creds *Credentials) context.Context {
	return context.WithValue(ctx, CredentialsContextKey{}, creds)
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
			return &Credentials{APIKey: []byte(key), Source: SourceClient}
		}
		// sk-ant-api-… is a legitimate client key. sk-ant-oat-… is a Claude
		// subscription (Claude.ai login) token — works against /v1/messages
		// only via Bearer + oauth beta header, no x-api-key. Forwarded so
		// the caller's subscription pays for their turns.
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
			if sub := CodexSubscriptionCreds(key, headers.Get(ChatGPTAccountIDHeader)); sub != nil {
				return sub
			}
		}
		// Reject Anthropic-shaped tokens (keys and OAuth bearers) so one
		// Bearer header isn't misidentified as creds for every provider.
		if key != "" && !auth.HasAPIKeyPrefix(key) && !strings.HasPrefix(key, "sk-ant-") {
			return &Credentials{APIKey: []byte(key), Source: SourceClient}
		}
	}
	return nil
}

// SubscriptionCredsFromToken returns subscription credentials for a bare
// token, or nil if it isn't a Claude subscription bearer. Anthropic-only.
func SubscriptionCredsFromToken(token string) *Credentials {
	if !strings.HasPrefix(token, SubscriptionTokenPrefix) {
		return nil
	}
	return &Credentials{APIKey: []byte(token), Source: SourceSubscription, OAuth: true}
}

// CodexSubscriptionCreds returns Codex subscription credentials for a
// ChatGPT-login JWT paired with its account id, or nil otherwise. Rejects
// router keys and OpenAI API keys; requires a non-empty account id since
// the Codex backend 401/403s without it. OpenAI-only.
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
		APIKey:    []byte(token),
		AccountID: []byte(accountID),
		Source:    SourceCodexSubscription,
		OAuth:     true,
	}
}

// codexCoveredModels is the fail-closed set of models the Codex CLI may serve
// through the caller's ChatGPT OAuth credential. Deliberately a curated
// allowlist, not "every OpenAI model": infrastructure-served OpenAI models
// share ProviderOpenAI with the native Codex family, but must use BYOK or the
// router deployment credential instead of chatgpt.com/backend-api/codex.
var codexCoveredModels = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}

// CodexCoveredModels returns a copy of the models a Codex subscription may serve.
func CodexCoveredModels() []string {
	return append([]string(nil), codexCoveredModels...)
}

// CodexSubscriptionCoversModel reports whether model may receive the caller's
// ChatGPT OAuth credential. Exact canonical IDs only; aliases are resolved
// before routing, and unknown/future models fail closed.
func CodexSubscriptionCoversModel(model string) bool {
	for _, covered := range codexCoveredModels {
		if model == covered {
			return true
		}
	}
	return false
}

// ApplyWIFTokenType marks the bearer as a workload attestation, not an upstream-issued token.
// Must be called after prep.Headers are copied so a client-supplied value can't suppress it.
func ApplyWIFTokenType(ctx context.Context, upstream *http.Request) {
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.AuthType != auth.AuthTypeWIF {
		return
	}
	upstream.Header.Set(auth.WIFTokenTypeHeader, auth.WIFTokenTypeValue)
}
