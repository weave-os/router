package sessionpin

import "time"

type ModelCacheState struct {
	ServedAt     time.Time `json:"served_at"`
	PromptTokens int       `json:"prompt_tokens"`
	MessageCount int       `json:"message_count,omitempty"`
}

type ModelCaches map[string]ModelCacheState
