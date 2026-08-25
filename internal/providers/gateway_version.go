package providers

import (
	"strings"
	"sync"
)

// versionSegment is the API version path segment OpenAI-spec and Anthropic-spec
// surfaces are mounted under.
const versionSegment = "/v1"

// GatewayVersionMemo resolves where a BYOK gateway actually mounts its API when
// the stored base URL disagrees with the adapter's canonical suffix — the
// Snowflake Cortex shape, where the catalog lives at {account}/api/v2/cortex/models
// but chat lives at {account}/api/v2/cortex/v1/chat/completions, so an admin who
// stores the base URL the catalog (or the Anthropic SDK) wants leaves the
// OpenAI-spec surface one "/v1" short. Base URLs are only ever probed, never
// rewritten: the alternate is tried after a 404 and remembered so later requests
// on the same endpoint skip the miss.
//
// The zero value is ready to use. Entries are bounded by the number of distinct
// gateway base URLs a deployment serves.
type GatewayVersionMemo struct {
	learned sync.Map // base URL -> struct{}
}

// URLs returns the upstream URLs to try for baseURL+suffix, likeliest first.
// A second entry appears only when the version segment is ambiguous: base URLs
// that already agree with suffix have nothing to fall back to.
func (m *GatewayVersionMemo) URLs(baseURL, suffix string) []string {
	primary := baseURL + suffix
	alt := altVersionedURL(baseURL, suffix)
	if alt == "" {
		return []string{primary}
	}
	if _, ok := m.learned.Load(baseURL); ok {
		return []string{alt, primary}
	}
	return []string{primary, alt}
}

// Learn records that baseURL serves the alternate URL, so URLs puts it first.
func (m *GatewayVersionMemo) Learn(baseURL string) {
	m.learned.Store(baseURL, struct{}{})
}

// altVersionedURL returns the URL that moves suffix's version segment across the
// base-URL boundary: appended when the base omits it, dropped from the base when
// the suffix already carries it. Empty when base and suffix already agree.
func altVersionedURL(baseURL, suffix string) string {
	root, baseVersioned := strings.CutSuffix(baseURL, versionSegment)
	suffixVersioned := strings.HasPrefix(suffix, versionSegment+"/")
	switch {
	case baseVersioned && suffixVersioned:
		return root + suffix
	case !baseVersioned && !suffixVersioned:
		return baseURL + versionSegment + suffix
	}
	return ""
}
