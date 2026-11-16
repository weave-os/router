package dispatch

import (
	"errors"
	"fmt"
	"sort"

	"weave-os/router/internal/providers"
)

var ErrProviderNotConfigured = errors.New("provider not configured")

type Clients struct {
	byName map[string]providers.Client
}

func NewClients(byName map[string]providers.Client) *Clients {
	copied := make(map[string]providers.Client, len(byName))
	for name, client := range byName {
		copied[name] = client
	}
	return &Clients{byName: copied}
}

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

func (c *Clients) Has(name string) bool {
	if c == nil {
		return false
	}
	_, ok := c.byName[name]
	return ok
}

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

func (c *Clients) Len() int {
	if c == nil {
		return 0
	}
	return len(c.byName)
}
