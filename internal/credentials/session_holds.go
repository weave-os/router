package credentials

import (
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"weave-os/router/internal/subscriptions"
)

type SubscriptionSessionHolds struct {
	once    sync.Once
	entries *lru.Cache[string, time.Time]
}

func (h *SubscriptionSessionHolds) cache() *lru.Cache[string, time.Time] {
	h.once.Do(func() { h.entries, _ = lru.New[string, time.Time](8192) })
	return h.entries
}

func (h *SubscriptionSessionHolds) Hold(owner string, pool subscriptions.Provider, until time.Time) {
	h.cache().Add(sessionHoldKey(owner, pool), until)
}

func (h *SubscriptionSessionHolds) Held(owner string, pool subscriptions.Provider, now time.Time) bool {
	until, ok := h.cache().Get(sessionHoldKey(owner, pool))
	return ok && now.Before(until)
}

func (h *SubscriptionSessionHolds) Release(owner string, pool subscriptions.Provider) bool {
	return h.cache().Remove(sessionHoldKey(owner, pool))
}

func sessionHoldKey(owner string, pool subscriptions.Provider) string {
	return owner + "\x00" + string(pool)
}
