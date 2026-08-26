package providers

import (
	"net/url"
	"strings"
)

// azureOpenAIHostSuffixes identify a resource that speaks the OpenAI wire format
// under /openai/v1: classic Azure OpenAI and Microsoft Foundry.
var azureOpenAIHostSuffixes = []string{".openai.azure.com", ".services.ai.azure.com"}

// azureOpenAIV1Path is where such a resource mounts the OpenAI v1 API. The
// resource root serves nothing, so a base URL stored the way an OpenAI-style
// endpoint would be answers 404 "Resource not found" on every path, inference
// and model discovery alike.
const azureOpenAIV1Path = "/openai/v1"

// NormalizeAzureOpenAIBaseURL rewrites an Azure resource base URL that points at
// the resource root or a bare /v1 onto the /openai/v1 surface. Any other path is
// returned unchanged: it names a deployment, a project, or a gateway route this
// adapter must not second-guess.
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
