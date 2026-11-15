package providers

import (
	"net/url"
	"strings"
)

var azureOpenAIHostSuffixes = []string{".openai.azure.com", ".services.ai.azure.com"}

const azureOpenAIV1Path = "/openai/v1"

func NormalizeAzureOpenAIBaseURL(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return baseURL
	}
	host := strings.ToLower(u.Hostname())
	azure := false
	for _, suffix := range azureOpenAIHostSuffixes {
		azure = azure || strings.HasSuffix(host, suffix)
	}
	switch path := strings.TrimSuffix(u.Path, "/"); {
	case !azure, path != "" && path != versionSegment:
		return baseURL
	}
	return u.Scheme + "://" + u.Host + azureOpenAIV1Path
}
