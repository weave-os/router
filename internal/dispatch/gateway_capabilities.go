package dispatch

import "sync"

// GatewayCapabilities memoizes what gateway endpoints told us they cannot
// serve, so only the first turn against such an endpoint pays the probe. Keys
// are endpoint identifiers chosen by the caller; the empty key is never
// memoized. The zero value is ready to use.
type GatewayCapabilities struct {
	noResponses      sync.Map
	noPromptCacheKey sync.Map
}

// LacksResponses reports whether that endpoint already told us it serves
// no Responses API.
func (g *GatewayCapabilities) LacksResponses(key string) bool {
	if key == "" {
		return false
	}
	_, ok := g.noResponses.Load(key)
	return ok
}

// RememberLacksResponses records a gateway's rejection of the Responses
// API so later tool turns go straight to chat/completions.
func (g *GatewayCapabilities) RememberLacksResponses(key string) {
	if key == "" {
		return
	}
	g.noResponses.Store(key, struct{}{})
}

// RejectsPromptCacheKey reports whether that endpoint already told us
// it refuses bodies carrying prompt_cache_key.
func (g *GatewayCapabilities) RejectsPromptCacheKey(key string) bool {
	if key == "" {
		return false
	}
	_, ok := g.noPromptCacheKey.Load(key)
	return ok
}

// RememberRejectsPromptCacheKey records a gateway's unknown-field
// rejection of prompt_cache_key so later turns go out without the hint.
func (g *GatewayCapabilities) RememberRejectsPromptCacheKey(key string) {
	if key == "" {
		return
	}
	g.noPromptCacheKey.Store(key, struct{}{})
}
