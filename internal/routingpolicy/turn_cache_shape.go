package routingpolicy

import "time"

type TurnCacheShape struct {
	MessageCount  int
	UserPrompt    bool
	SideRequest   bool
	CacheWriteTTL time.Duration
}
