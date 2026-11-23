package sessionpin

import "time"

type ModelCacheState struct {
	ServedAt        time.Time `json:"served_at"`
	PromptTokens    int       `json:"prompt_tokens"`
	MessageCount    int       `json:"message_count,omitempty"`
	WriteTTLSeconds int       `json:"write_ttl_s,omitempty"`
}

type ModelCaches map[string]ModelCacheState

func (u Usage) ModelCache(endedAt time.Time) (ModelCacheState, bool) {
	if u.ServedModel == "" || u.PromptTokens <= 0 {
		return ModelCacheState{}, false
	}
	return ModelCacheState{
		ServedAt:        endedAt.UTC(),
		PromptTokens:    u.PromptTokens,
		MessageCount:    u.MessageCount,
		WriteTTLSeconds: int(u.CacheWriteTTL / time.Second),
	}, true
}
