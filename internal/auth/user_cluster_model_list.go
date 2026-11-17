package auth

import "context"

type UserClusterModelList struct {
	RouterUserID string
	ClusterLabel string
	Models       []string
}

type UserClusterModelListRepository interface {
	GetForUser(ctx context.Context, routerUserID string) ([]UserClusterModelList, error)
}
