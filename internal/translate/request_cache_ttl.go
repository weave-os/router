package translate

import (
	"bytes"
	"time"
)

func (e *RequestEnvelope) CacheWriteTTL() time.Duration {
	if e == nil || !bytes.Contains(e.body, []byte(`"cache_control"`)) {
		return 0
	}
	if AnthropicRequestCacheTTL1h(e.body) {
		return time.Hour
	}
	return 5 * time.Minute
}
