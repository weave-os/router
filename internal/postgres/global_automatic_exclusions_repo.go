package postgres

import (
	"context"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/sqlc"
)

// GlobalAutomaticExclusionRepo reads the deployment-wide models the Weave
// control plane has withdrawn from automatic routing. Read-only here: the rows
// are written by the control plane, which owns catalog validation.
type GlobalAutomaticExclusionRepo struct {
	tx sqlc.DBTX
}

func NewGlobalAutomaticExclusionRepo(tx sqlc.DBTX) *GlobalAutomaticExclusionRepo {
	return &GlobalAutomaticExclusionRepo{tx: tx}
}

var _ proxy.GlobalAutomaticExclusionStore = (*GlobalAutomaticExclusionRepo)(nil)

// ListGlobalAutomaticRoutingExclusions returns each disabled model mapped to
// the operator's reason, which is empty when none was recorded.
func (r *GlobalAutomaticExclusionRepo) ListGlobalAutomaticRoutingExclusions(ctx context.Context) (map[string]string, error) {
	rows, err := sqlc.New(r.tx).ListGlobalAutomaticRoutingExclusions(ctx)
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
