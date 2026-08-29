package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// EntraScope is the Microsoft Entra scope used by Foundry inference endpoints.
const EntraScope = "https://ai.azure.com/.default"

// EntraScopeCognitiveServices is the scope classic Azure OpenAI resources expect;
// a token minted with EntraScope is rejected with 401 on *.openai.azure.com.
const EntraScopeCognitiveServices = "https://cognitiveservices.azure.com/.default"

// azureOpenAIHostSuffix identifies a classic Azure OpenAI resource endpoint.
const azureOpenAIHostSuffix = ".openai.azure.com"

// EntraScopeForBaseURL returns the token scope a key's endpoint accepts.
func EntraScopeForBaseURL(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return EntraScope
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.HasSuffix(host, azureOpenAIHostSuffix) {
		return EntraScopeCognitiveServices
	}
	return EntraScope
}

// ErrEntraUnavailable is returned when an Azure Entra token cannot be obtained.
var ErrEntraUnavailable = errors.New("auth: Microsoft Entra token is unavailable")

// EntraTokenSource mints a short-lived Microsoft Entra token for a BYOK key.
// The key carries the tenant ID, client ID, and encrypted client secret.
type EntraTokenSource interface {
	Token(context.Context, *ExternalAPIKey) ([]byte, error)
}
