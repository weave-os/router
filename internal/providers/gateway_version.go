package providers

import (
	"strings"
	"sync"
)

const versionSegment = "/v1"

type GatewayVersionMemo struct {
	learned sync.Map
}

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

func (m *GatewayVersionMemo) Learn(baseURL string) {
	m.learned.Store(baseURL, struct{}{})
}

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
