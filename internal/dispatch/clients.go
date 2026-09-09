// Package dispatch is the single execution boundary between policy-resolved
// plans and provider adapters. It owns the provider client registry, walks a
// plan's targets with the retry/failover rules, checks that the prepared wire
// request names the plan target before any upstream I/O, and emits ordered
// attempt events and the operation summary. Feature packages depend on
// inference.Executor; only this package dereferences providers.Client.
package dispatch

import (
	"errors"
	"fmt"
	"sort"

	"weave-os/router/internal/providers"
)

// ErrProviderNotConfigured is returned when a target names a provider that
// has no client in this deployment.
var ErrProviderNotConfigured = errors.New("provider not configured")

// Clients is the read-only registry of provider clients wired at boot.
type Clients struct {
	byName map[string]providers.Client
}

// NewClients copies the boot-time provider map so later mutation of the
// caller's map cannot change dispatch behavior. A name mapped to a nil client
// counts as registered for eligibility but can never be dispatched to.
func NewClients(byName map[string]providers.Client) *Clients {
	copied := make(map[string]providers.Client, len(byName))
	for name, client := range byName {
		copied[name] = client
	}
	return &Clients{byName: copied}
}

// Client returns the adapter registered for provider name.
func (c *Clients) Client(name string) (providers.Client, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotConfigured, name)
	}
	client, ok := c.byName[name]
	if !ok || client == nil {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotConfigured, name)
	}
	return client, nil
}

// Has reports whether provider name has a registered client.
func (c *Clients) Has(name string) bool {
	if c == nil {
		return false
	}
	_, ok := c.byName[name]
	return ok
}

// Names returns the registered provider names in sorted order.
func (c *Clients) Names() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.byName))
	for name := range c.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NameSet returns the registered provider names as a set.
func (c *Clients) NameSet() map[string]struct{} {
	if c == nil {
		return map[string]struct{}{}
	}
	set := make(map[string]struct{}, len(c.byName))
	for name := range c.byName {
		set[name] = struct{}{}
	}
	return set
}

// Len returns the number of registered providers.
func (c *Clients) Len() int {
	if c == nil {
		return 0
	}
	return len(c.byName)
}
