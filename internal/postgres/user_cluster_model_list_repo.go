package postgres

import (
	"context"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
)

type userClusterModelListRepo struct {
	tx sqlc.DBTX
}

// NewUserClusterModelListRepo constructs a per-user per-cluster selection repo.
func NewUserClusterModelListRepo(tx sqlc.DBTX) auth.UserClusterModelListRepository {
	return &userClusterModelListRepo{tx: tx}
}

func (r *userClusterModelListRepo) GetForUser(ctx context.Context, routerUserID string) ([]auth.UserClusterModelList, error) {
	parsed, err := uuid.Parse(routerUserID)
	if err != nil {
		return nil, err
	}
	q := sqlc.New(r.tx)
	rows, err := q.GetUserClusterModelListsByUser(ctx, parsed)
	if err != nil {
		return nil, err
	}
	out := make([]auth.UserClusterModelList, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAuthUserClusterModelList(row))
	}
	return out, nil
}

func toAuthUserClusterModelList(row sqlc.RouterModelRouterUserClusterModelList) auth.UserClusterModelList {
	return auth.UserClusterModelList{
		RouterUserID: row.RouterUserID.String(),
		ClusterLabel: row.ClusterLabel,
		Models:       row.Models,
	}
}
