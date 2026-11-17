package dispatch

import "sync"

type GatewayCapabilities struct {
	noResponses      sync.Map
	noPromptCacheKey sync.Map
}

func (g *GatewayCapabilities) LacksResponses(key string) bool {
	if key == "" {
		return false
	}
	_, ok := g.noResponses.Load(key)
	return ok
}

func (g *GatewayCapabilities) RememberLacksResponses(key string) {
	if key == "" {
		return
	}
	g.noResponses.Store(key, struct{}{})
}

func (g *GatewayCapabilities) RejectsPromptCacheKey(key string) bool {
	if key == "" {
		return false
	}
	_, ok := g.noPromptCacheKey.Load(key)
	return ok
}

func (g *GatewayCapabilities) RememberRejectsPromptCacheKey(key string) {
	if key == "" {
		return
	}
	g.noPromptCacheKey.Store(key, struct{}{})
}
