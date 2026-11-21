package overrides

import "weave-os/router/internal/router/policy"

type Resolver func() (handled bool, err error)

func Resolve(precedence []policy.OverrideSource, resolvers map[policy.OverrideSource][]Resolver) (bool, error) {
	for _, source := range precedence {
		for _, resolve := range resolvers[source] {
			if handled, err := resolve(); handled || err != nil {
				return handled, err
			}
		}
	}
	return false, nil
}
