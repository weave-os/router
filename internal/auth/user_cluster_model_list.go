package auth

import "context"

// UserClusterModelList is one router user's ordered model selection for a
// single cluster. Models is serving priority order (index 0 = highest). Empty
// slices are never persisted — the DB enforces cardinality > 0, so "the user
// cleared this cluster" is a deleted row, not an empty list.
//
// Distinct from ClusterModelList (keyed by API key) rather than a reuse: a key
// is an installation-scoped secret shared by every user on it, so the two are
// different scopes that compose — the key-scoped list is the org default and
// this narrows it per user.
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
