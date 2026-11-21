package postgres

import (
	"context"

	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/sqlc"
)

type GlobalAutomaticExclusionRepo struct {
	tx sqlc.DBTX
}

func NewGlobalAutomaticExclusionRepo(tx sqlc.DBTX) *GlobalAutomaticExclusionRepo {
	return &GlobalAutomaticExclusionRepo{tx: tx}
}

var _ candidates.GlobalAutomaticExclusionStore = (*GlobalAutomaticExclusionRepo)(nil)

func (r *GlobalAutomaticExclusionRepo) ListGlobalAutomaticRoutingExclusions(ctx context.Context) (map[string]string, error) {
	rows, err := dbbudget.Queries(r.tx).ListGlobalAutomaticRoutingExclusions(ctx)
	if err != nil {
		return nil, err
	}
	byModel := make(map[string]string, len(rows))
	for _, row := range rows {
		reason := ""
		if row.Reason != nil {
			reason = *row.Reason
		}
		byModel[row.Model] = reason
	}
	return byModel, nil
}
