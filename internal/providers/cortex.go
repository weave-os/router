package providers

import (
	"net/url"
	"strings"
)

const cortexHostSuffix = ".snowflakecomputing.com"

const cortexRESTPath = "/api/v2/cortex"

const aiGatewaysPath = "/api/v2/aigateways/"

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

func IsCortexBaseURL(baseURL string) bool {
	_, _, _, ok := cortexInferenceRoot(baseURL)
	return ok
}

func NormalizeCortexOpenAIBaseURL(baseURL string) string {
	u, root, unversioned, ok := cortexInferenceRoot(baseURL)
	if !ok || !unversioned {
		return baseURL
	}
	return u.Scheme + "://" + u.Host + root + versionSegment
}

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
