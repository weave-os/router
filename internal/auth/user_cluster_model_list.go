package auth

import "context"

// UserClusterModelList is one router user's ordered model selection for a single
// cluster. Models is serving-priority order (index 0 = highest). Empty slices are
// never persisted (DB enforces cardinality > 0). Distinct from ClusterModelList
// (keyed by API key, shared across all users) — the two compose: key list is the
// org default, this narrows it per user.
type UserClusterModelList struct {
	RouterUserID string
	ClusterLabel string
	Models       []string
}

// UserClusterModelListRepository reads per-user per-cluster ordered selections.
// Writes are control-plane-owned (direct inserts); the router is read-only on
// the auth path.
type UserClusterModelListRepository interface {
	// GetForUser returns every configured cluster selection for a router user.
	GetForUser(ctx context.Context, routerUserID string) ([]UserClusterModelList, error)
}
