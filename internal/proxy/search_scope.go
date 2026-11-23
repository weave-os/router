package proxy

import (
	"context"
	"sync"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

type searchUseTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, int]
}

func newSearchUseTracker() *searchUseTracker {
	return &searchUseTracker{
		cache: lru.NewLRU[string, int](conversation.NoProgressCacheSize, nil, conversation.NoProgressCacheTTL),
	}
}

func (t *searchUseTracker) observe(key string, recency, decayTurns int) bool {
	if t == nil || t.cache == nil {
		return recency == 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	remaining := 0
	if prior, ok := t.cache.Get(key); ok && prior > 0 {
		remaining = prior - 1
	}
	if recency >= 0 && decayTurns-recency > remaining {
		remaining = decayTurns - recency
	}
	t.cache.Add(key, remaining)
	return recency == 0 || remaining > 0
}

func (s *Service) scopeSearchRequirement(sessionKey [sessionpin.SessionKeyLen]byte, env *translate.RequestEnvelope, reqs router.TranslationRequirements) router.TranslationRequirements {
	if !s.ingress.ScopedSearchRequirement || env == nil || !reqs.CitationsOrSearch ||
		reqs.SourceFormat == router.WireFormatGemini {
		return reqs
	}
	reqs.CitationsOrSearch = s.searchUse.observe(
		string(sessionKey[:]), env.SearchToolUseRecency(), s.ingress.SearchRequirementDecayTurns)
	return reqs
}

func searchUseEnvelope(ctx context.Context, env *translate.RequestEnvelope) *translate.RequestEnvelope {
	native, ok := ctx.Value(nativeResponsesBodyContextKey{}).([]byte)
	if !ok || len(native) == 0 {
		return env
	}
	nativeEnv, err := translate.ParseOpenAI(native)
	if err != nil {
		return env
	}
	return nativeEnv
}
