package providers

import (
	"net/url"
	"strings"
)

// snowflakeCortexHostSuffix identifies a Snowflake account host whose Cortex
// REST API speaks the OpenAI wire format under /api/v2/cortex/v1.
const snowflakeCortexHostSuffix = ".snowflakecomputing.com"

// snowflakeCortexPath is the Cortex REST root customers typically store as the
// gateway base URL; the OpenAI-spec surface hangs one "/v1" below it.
const snowflakeCortexPath = "/api/v2/cortex"

// NormalizeSnowflakeCortexOpenAIBaseURL rewrites a Snowflake Cortex REST root
// onto its OpenAI-spec surface (/api/v2/cortex/v1); other hosts/paths pass through.
func NormalizeSnowflakeCortexOpenAIBaseURL(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return baseURL
	}
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), snowflakeCortexHostSuffix) {
		return baseURL
	}
	if strings.TrimSuffix(u.Path, "/") != snowflakeCortexPath {
		return baseURL
	}
	return u.Scheme + "://" + u.Host + snowflakeCortexPath + versionSegment
}
