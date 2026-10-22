package providers

import (
	"net/url"
	"strings"
)

// cortexHostSuffix identifies an account host whose Cortex
// inference surfaces speak the OpenAI and Anthropic wire formats under "/v1".
const cortexHostSuffix = ".snowflakecomputing.com"

// cortexRESTPath is the Cortex REST root customers typically store as the
// gateway base URL; the OpenAI-spec surface hangs one "/v1" below it.
const cortexRESTPath = "/api/v2/cortex"

// aiGatewaysPath prefixes a named Cortex AI Gateway root
// (/api/v2/aigateways/<name>), which mounts the same "/v1" surfaces.
const aiGatewaysPath = "/api/v2/aigateways/"

// cortexInferenceRoot returns baseURL's Cortex REST or AI Gateway root path
// and whether its path ends at that root (no version segment).
func cortexInferenceRoot(baseURL string) (u *url.URL, root string, unversioned, ok bool) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, "", false, false
	}
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), cortexHostSuffix) {
		return nil, "", false, false
	}
	path := strings.TrimSuffix(u.Path, "/")
	root = strings.TrimSuffix(path, versionSegment)
	if root != cortexRESTPath && !isAIGatewayPath(root) {
		return nil, "", false, false
	}
	return u, root, root == path, true
}

func isAIGatewayPath(path string) bool {
	name, ok := strings.CutPrefix(path, aiGatewaysPath)
	return ok && name != "" && !strings.Contains(name, "/")
}

// IsCortexBaseURL reports whether baseURL targets a Cortex REST or AI
// Gateway OpenAI surface, before or after the version segment has been normalized.
func IsCortexBaseURL(baseURL string) bool {
	_, _, _, ok := cortexInferenceRoot(baseURL)
	return ok
}

// NormalizeCortexOpenAIBaseURL rewrites a Cortex REST or AI
// Gateway root onto its OpenAI-spec surface ("/v1"); other hosts/paths pass through.
func NormalizeCortexOpenAIBaseURL(baseURL string) string {
	u, root, unversioned, ok := cortexInferenceRoot(baseURL)
	if !ok || !unversioned {
		return baseURL
	}
	return u.Scheme + "://" + u.Host + root + versionSegment
}

// CortexRESTRoot maps a Cortex REST or AI Gateway base URL, with or
// without "/v1", onto the account's Cortex REST root. The host is not checked.
func CortexRESTRoot(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return baseURL
	}
	path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), versionSegment)
	if isAIGatewayPath(path) {
		path = cortexRESTPath
	}
	u.Path, u.RawPath = path, ""
	return u.String()
}
