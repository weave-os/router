package proxy

import (
	"sync"

	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// DefaultSearchRequirementDecayTurns is how many routed turns after the last
// actual search-tool use keep the citations/search native requirement alive.
const DefaultSearchRequirementDecayTurns = 3

// searchUseTracker is a per-session decay counter for the native-search requirement.
//
// mu serialises observe's Get+Add so concurrent turns on one session can't both read the same prior counter.
type searchUseTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, int]
}

func newSearchUseTracker() *searchUseTracker {
	return &searchUseTracker{
		cache: lru.NewLRU[string, int](noProgressCacheSize, nil, noProgressCacheTTL),
	}
}

// observe folds recency into the session's decay counter and reports whether the
// requirement is still active (current-turn use, or fewer than decayTurns turns since last use).
// The counter covers uses the client's history no longer carries (compaction, previous_response_id).
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

// scopeSearchRequirement narrows CitationsOrSearch from tool-advertisement to actual recent use,
// returning advertised-only turns to policy routing. Gemini is excluded: a declared googleSearch
// tool grounds every response, so advertisement is use.
func (s *Service) scopeSearchRequirement(sessionKey [sessionpin.SessionKeyLen]byte, env *translate.RequestEnvelope, reqs router.TranslationRequirements) router.TranslationRequirements {
	if !s.scopedSearchRequirement || env == nil || !reqs.CitationsOrSearch ||
		reqs.SourceFormat == router.WireFormatGemini {
		return reqs
	}
	reqs.CitationsOrSearch = s.searchUse.observe(
		string(sessionKey[:]), env.SearchToolUseRecency(), s.searchRequirementDecayTurns)
	return reqs
}
