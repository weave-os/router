package auth

import (
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

type InstallationInvalidator interface {
	InvalidateInstallation(installationID string)
}

type UserClusterListCache interface {
	Get(routerUserID string) ([]UserClusterModelList, bool)
	Set(installationID, routerUserID string, lists []UserClusterModelList)
	InvalidateInstallation(installationID string)
}

type NoOpUserClusterListCache struct{}

func (NoOpUserClusterListCache) Get(string) ([]UserClusterModelList, bool) { return nil, false }
func (NoOpUserClusterListCache) Set(string, string, []UserClusterModelList) {
}
func (NoOpUserClusterListCache) InvalidateInstallation(string) {}

type LRUUserClusterListCache struct {
	mu                 sync.Mutex
	entries            *expirable.LRU[string, []UserClusterModelList]
	byInstallation     map[string]map[string]struct{}
	invalidationGen    map[string]uint64
	installationByUser map[string]string
}

func NewLRUUserClusterListCache(size int, ttl time.Duration) *LRUUserClusterListCache {
	c := &LRUUserClusterListCache{
		byInstallation:     make(map[string]map[string]struct{}),
		invalidationGen:    make(map[string]uint64),
		installationByUser: make(map[string]string),
	}
	c.entries = expirable.NewLRU(size, c.onEvict, ttl)
	return c
}

func (c *LRUUserClusterListCache) Get(routerUserID string) ([]UserClusterModelList, bool) {
	return c.entries.Get(routerUserID)
}

func (c *LRUUserClusterListCache) Set(installationID, routerUserID string, lists []UserClusterModelList) {
	if routerUserID == "" {
		return
	}
	var preGen uint64
	c.mu.Lock()
	if installationID != "" {
		preGen = c.invalidationGen[installationID]
		users, ok := c.byInstallation[installationID]
		if !ok {
			users = make(map[string]struct{}, 1)
			c.byInstallation[installationID] = users
		}
		users[routerUserID] = struct{}{}
		c.installationByUser[routerUserID] = installationID
	}
	c.mu.Unlock()
	c.entries.Add(routerUserID, lists)
	if installationID == "" {
		return
	}
	c.mu.Lock()
	if c.invalidationGen[installationID] != preGen {
		c.removeFromIndexLocked(installationID, routerUserID)
		c.mu.Unlock()
		c.entries.Remove(routerUserID)
		return
	}
	c.mu.Unlock()
}

func (c *LRUUserClusterListCache) InvalidateInstallation(installationID string) {
	if installationID == "" {
		return
	}
	c.mu.Lock()
	users := c.byInstallation[installationID]
	delete(c.byInstallation, installationID)
	c.invalidationGen[installationID]++
	for user := range users {
		delete(c.installationByUser, user)
	}
	c.mu.Unlock()
	for user := range users {
		c.entries.Remove(user)
	}
}

func (c *LRUUserClusterListCache) onEvict(routerUserID string, _ []UserClusterModelList) {
	c.mu.Lock()
	defer c.mu.Unlock()
	installationID, ok := c.installationByUser[routerUserID]
	if !ok {
		return
	}
	c.removeFromIndexLocked(installationID, routerUserID)
}

func (c *LRUUserClusterListCache) removeFromIndexLocked(installationID, routerUserID string) {
	delete(c.installationByUser, routerUserID)
	users, ok := c.byInstallation[installationID]
	if !ok {
		return
	}
	delete(users, routerUserID)
	if len(users) == 0 {
		delete(c.byInstallation, installationID)
	}
}

var (
	_ InstallationInvalidator = (*LRUAPIKeyCache)(nil)
	_ InstallationInvalidator = (*LRUUserClusterListCache)(nil)
	_ UserClusterListCache    = (*LRUUserClusterListCache)(nil)
	_ UserClusterListCache    = NoOpUserClusterListCache{}
)
