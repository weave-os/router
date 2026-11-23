package sessionpin

import (
	"strings"
	"time"
)

type ModelCacheState struct {
	ServedAt        time.Time `json:"served_at"`
	PromptTokens    int       `json:"prompt_tokens"`
	MessageCount    int       `json:"message_count,omitempty"`
	WriteTTLSeconds int       `json:"write_ttl_s,omitempty"`
}

type ModelCaches map[string]ModelCacheState

func BaseModelOf(servedIdentity string) string {
	if idx := strings.LastIndex(servedIdentity, ":"); idx > 0 {
		return servedIdentity[:idx]
	}
	return servedIdentity
}

func (u Usage) ModelCache(endedAt time.Time) (string, ModelCacheState, bool) {
	if u.ServedModel == "" || u.PromptTokens <= 0 {
		return "", ModelCacheState{}, false
	}
	return BaseModelOf(u.ServedModel), ModelCacheState{
		ServedAt:        endedAt.UTC(),
		PromptTokens:    u.PromptTokens,
		MessageCount:    u.MessageCount,
		WriteTTLSeconds: int(u.CacheWriteTTL / time.Second),
	}, true
}
