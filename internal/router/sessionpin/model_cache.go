package sessionpin

import "time"

type ModelCacheState struct {
	ServedAt        time.Time `json:"served_at"`
	PromptTokens    int       `json:"prompt_tokens"`
	MessageCount    int       `json:"message_count,omitempty"`
	WriteTTLSeconds int       `json:"write_ttl_s,omitempty"`
}

type ModelCaches map[string]ModelCacheState
