package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

const EntraScope = "https://ai.azure.com/.default"

const EntraScopeCognitiveServices = "https://cognitiveservices.azure.com/.default"

const azureOpenAIHostSuffix = ".openai.azure.com"

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

var ErrEntraUnavailable = errors.New("auth: Microsoft Entra token is unavailable")

type EntraTokenSource interface {
	Token(context.Context, *ExternalAPIKey) ([]byte, error)
}

func (s *Service) WithEntraTokenSource(src EntraTokenSource) *Service {
	s.entraTokens = src
	return s
}
