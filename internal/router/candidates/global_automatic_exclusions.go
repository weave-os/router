package candidates

import "context"

type GlobalAutomaticExclusionStore interface {
	ListGlobalAutomaticRoutingExclusions(ctx context.Context) (map[string]string, error)
}
