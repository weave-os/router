package credentials

import (
	"context"
	"hash/maphash"
	"slices"
	"sync"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	lru "github.com/hashicorp/golang-lru/v2"
)

const SubscriptionModelDenialTTL = 15 * time.Minute

const SubscriptionModelServedTTL = 6 * time.Hour

type subscriptionModelKey struct {
	token    uint64
	owner    string
	account  string
	provider string
	model    string
}

type SubscriptionModelAccess struct {
	once    sync.Once
	seed    maphash.Seed
	entries *lru.Cache[subscriptionModelKey, time.Time]

	servedMu sync.Mutex
	served   *lru.Cache[uint64, map[string]time.Time]
}

func (a *SubscriptionModelAccess) cache() *lru.Cache[subscriptionModelKey, time.Time] {
	a.once.Do(func() {
		a.seed = maphash.MakeSeed()
		a.entries, _ = lru.New[subscriptionModelKey, time.Time](4096)
		a.served, _ = lru.New[uint64, map[string]time.Time](4096)
	})
	return a.entries
}

func (a *SubscriptionModelAccess) key(token []byte, model string) subscriptionModelKey {
	return a.keyForProvider(token, "", model)
}

func (a *SubscriptionModelAccess) keyForProvider(token []byte, provider, model string) subscriptionModelKey {
	a.cache()
	return subscriptionModelKey{token: maphash.Bytes(a.seed, token), provider: provider, model: router.StripDateSuffix(model)}
}

func (a *SubscriptionModelAccess) managedKey(owner, account, provider, model string) subscriptionModelKey {
	return subscriptionModelKey{owner: owner, account: account, provider: provider, model: router.StripDateSuffix(model)}
}

func (a *SubscriptionModelAccess) Denied(token []byte, model string, now time.Time) bool {
	return a.DeniedForProvider(token, "", model, now)
}

func (a *SubscriptionModelAccess) DeniedForCaller(ctx context.Context, model string, now time.Time) bool {
	creds := requestcontext.CredentialsFromContext(ctx)
	return creds != nil && creds.OAuth && a.Denied(creds.APIKey, model, now)
}

func (a *SubscriptionModelAccess) markServed(token []byte, model string, now time.Time) {
	a.cache()
	key := maphash.Bytes(a.seed, token)
	a.servedMu.Lock()
	defer a.servedMu.Unlock()
	prev, _ := a.served.Get(key)
	next := map[string]time.Time{router.StripDateSuffix(model): now}
	for id, at := range prev {
		if _, ok := next[id]; !ok && now.Sub(at) < SubscriptionModelServedTTL {
			next[id] = at
		}
	}
	a.served.Add(key, next)
}

func (a *SubscriptionModelAccess) ServedForCaller(ctx context.Context, now time.Time) []string {
	creds := requestcontext.CredentialsFromContext(ctx)
	if creds == nil || !creds.OAuth || len(creds.APIKey) == 0 {
		return nil
	}
	a.cache()
	a.servedMu.Lock()
	models, _ := a.served.Get(maphash.Bytes(a.seed, creds.APIKey))
	a.servedMu.Unlock()
	var out []string
	for id, at := range models {
		if now.Sub(at) < SubscriptionModelServedTTL {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func (a *SubscriptionModelAccess) DeniedForProvider(token []byte, provider, model string, now time.Time) bool {
	until, ok := a.cache().Get(a.keyForProvider(token, provider, model))
	return ok && now.Before(until)
}

func (a *SubscriptionModelAccess) ManagedDenied(owner, account, provider, model string, now time.Time) bool {
	until, ok := a.cache().Get(a.managedKey(owner, account, provider, model))
	return ok && now.Before(until)
}

func (a *SubscriptionModelAccess) DenyManaged(owner, account, provider, model string, until time.Time) {
	a.cache().Add(a.managedKey(owner, account, provider, model), until)
}

func (a *SubscriptionModelAccess) MarkServedForCaller(ctx context.Context, provider, model string, now time.Time) {
	if creds := requestcontext.CredentialsFromContext(ctx); provider == providers.ProviderAnthropic && creds != nil && creds.OAuth && len(creds.APIKey) > 0 {
		a.markServed(creds.APIKey, model, now)
	}
}

func thirdPartyDenialModel(clientApp string) string {
	return "\x00client-app:" + clientApp
}

func (a *SubscriptionModelAccess) DenyManagedClientApp(account, provider, clientApp string, until time.Time) {
	a.DenyManaged("account:"+account, account, provider, thirdPartyDenialModel(clientApp), until)
}

func (a *SubscriptionModelAccess) ManagedClientAppDenied(account, provider, clientApp string, now time.Time) bool {
	return a.ManagedDenied("account:"+account, account, provider, thirdPartyDenialModel(clientApp), now)
}

func (a *SubscriptionModelAccess) Deny(token []byte, model string, until time.Time) {
	a.cache().Add(a.key(token, model), until)
}

func (a *SubscriptionModelAccess) DenyForProvider(token []byte, provider, model string, until time.Time) {
	a.cache().Add(a.keyForProvider(token, provider, model), until)
}
