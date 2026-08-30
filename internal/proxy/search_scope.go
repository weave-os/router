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

// searchUseTracker keeps a per-session decay counter of turns remaining on the
// native-search requirement, mirroring the other in-memory session trackers.
//
// mu makes observe's Get+Add a single step so concurrent turns on one session
// can't both read the same prior counter.
type searchUseTracker struct {
	mu    sync.Mutex
	cache *lru.LRU[string, int]
}

func newSearchUseTracker() *searchUseTracker {
	return &searchUseTracker{
		cache: lru.NewLRU[string, int](noProgressCacheSize, nil, noProgressCacheTTL),
	}
}

// observe folds this turn's actual-use recency (see
// translate.SearchToolUseRecency) into the session's decay counter and reports
// whether the requirement is still active: the current turn searched, or fewer
// than decayTurns routed turns have passed since the last actual use. The
// counter also covers uses the client's history no longer carries (compaction,
// Responses previous_response_id).
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

// scopeSearchRequirement narrows CitationsOrSearch from "a native search tool
// is advertised" to "this session actually used one recently", returning
// advertised-only turns to ordinary policy routing. Genuine search turns keep
// the native-capability constraint, within which the policy router still
// selects the highest-ranked eligible roster arm. Gemini stays unscoped: a
// declared googleSearch tool grounds every response, so advertisement there is
// use.
func (s *Service) scopeSearchRequirement(sessionKey [sessionpin.SessionKeyLen]byte, env *translate.RequestEnvelope, reqs router.TranslationRequirements) router.TranslationRequirements {
	if !s.scopedSearchRequirement || env == nil || !reqs.CitationsOrSearch ||
		reqs.SourceFormat == router.WireFormatGemini {
		return reqs
	}
	reqs.CitationsOrSearch = s.searchUse.observe(
		string(sessionKey[:]), env.SearchToolUseRecency(), s.searchRequirementDecayTurns)
	return reqs
}
