package providers

import (
	"net/url"
	"strings"
)

// azureOpenAIHostSuffixes identify a resource that speaks the OpenAI wire format
// under /openai/v1: classic Azure OpenAI and Microsoft Foundry.
var azureOpenAIHostSuffixes = []string{".openai.azure.com", ".services.ai.azure.com"}

// azureOpenAIV1Path is the surface Azure resource hosts actually serve; root
// and bare /v1 answer 404 "Resource not found".
const azureOpenAIV1Path = "/openai/v1"

// NormalizeAzureOpenAIBaseURL rewrites a bare or /v1 Azure resource URL onto
// /openai/v1; any other path is returned unchanged to avoid clobbering a
// deployment, project, or gateway route the caller chose explicitly.
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
