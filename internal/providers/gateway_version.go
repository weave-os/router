package providers

import (
	"strings"
	"sync"
)

// versionSegment is the API version path segment OpenAI-spec and Anthropic-spec
// surfaces are mounted under.
const versionSegment = "/v1"

// GatewayVersionMemo resolves gateway base URLs that disagree with an adapter's
// canonical suffix on the "/v1" segment, probing then memoizing the alternate.
type GatewayVersionMemo struct {
	learned sync.Map // base URL -> struct{}
}

// URLs returns the upstream URLs to try for baseURL+suffix, likeliest first.
// A second entry appears only when base and suffix disagree on the "/v1" segment.
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

// altVersionedURL returns the alternate URL when base and suffix disagree on the
// "/v1" segment. Empty when they already agree.
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
