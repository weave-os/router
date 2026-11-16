package auth

import "context"

type ClusterModelList struct {
	APIKeyID     string
	ClusterLabel string
	Models       []string
}

type ClusterModelListRepository interface {
	GetForAPIKey(ctx context.Context, apiKeyID string) ([]ClusterModelList, error)
}
