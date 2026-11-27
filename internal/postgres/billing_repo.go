package postgres

import (
	"context"
	"errors"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type BillingRepo struct {
	tx sqlc.DBTX
}

func NewBillingRepo(tx sqlc.DBTX) *BillingRepo {
	return &BillingRepo{tx: tx}
}

var _ billing.Repo = (*BillingRepo)(nil)

func (r *BillingRepo) GetBalance(ctx context.Context, orgID string) (int64, error) {
	q := dbbudget.Queries(r.tx)
	balance, err := q.GetOrgCreditBalance(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, billing.ErrBalanceRowMissing
		}
		return 0, err
	}
	return balance, nil
}

func (r *BillingRepo) HasActiveOverride(ctx context.Context, orgID string) (bool, error) {
	q := dbbudget.Queries(r.tx)
	override, err := q.GetActiveBillingOverride(ctx, orgID)
	if err != nil {
		return false, err
	}
	return override, nil
}

func (r *BillingRepo) DebitInference(ctx context.Context, p billing.DebitParams) (int64, error) {
	q := dbbudget.Queries(r.tx)
	feeEntryType := p.FeeEntryType
	if p.FeeUsdMicros == 0 {
		feeEntryType = p.EntryType
	}
	row, err := q.DebitOrgCredits(ctx, sqlc.DebitOrgCreditsParams{
		OrganizationID:        p.OrganizationID,
		ChargedUsdMicros:      p.DeltaUsdMicros + p.FeeUsdMicros,
		UnfundedEntryType:     billing.EntryTypeUnfunded,
		BalanceFloorUsdMicros: billing.OrganizationBalanceFloorMicros,
		DeltaUsdMicros:        p.DeltaUsdMicros,
		NotionalCostMicros:    p.NotionalCostMicros,
		EntryType:             p.EntryType,
		FeeUsdMicros:          p.FeeUsdMicros,
		FeeEntryType:          feeEntryType,
		RouterRequestID:       stringPtrOrNil(p.RouterRequestID),
		RouterModel:           stringPtrOrNil(p.RouterModel),
		APIKeyID:              uuidOrNil(p.APIKeyID),
		DelegatedSessionID:    stringPtrOrNil(p.DelegatedSessionID),
		DelegatedTaskID:       stringPtrOrNil(p.DelegatedTaskID),
		RouterUserID:          uuidOrNil(p.RouterUserID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, billing.ErrBalanceRowMissing
	}
	if err != nil {
		return 0, err
	}
	if !row.Applied {
		return row.BalanceAfterMicros, billing.ErrBalanceFloorExceeded
	}
	return row.BalanceAfterMicros, nil
}

func (r *BillingRepo) GetDelegatedTokenSpend(ctx context.Context, issuerKeyID, sessionID, taskID string) (int64, int64, error) {
	issuer, err := uuid.Parse(issuerKeyID)
	if err != nil {
		return 0, 0, err
	}
	row, err := dbbudget.Queries(r.tx).GetDelegatedTokenSpend(ctx, sqlc.GetDelegatedTokenSpendParams{IssuerKeyID: issuer, SessionID: sessionID, TaskID: taskID})
	if err != nil {
		return 0, 0, err
	}
	return row.SessionSpentUsdMicros, row.TaskSpentUsdMicros, nil
}

func (r *BillingRepo) GetAPIKeySpend(ctx context.Context, apiKeyID string) (int64, *int64, bool, error) {
	parsed, ok := parseOptionalUUID(apiKeyID)
	if !ok {
		return 0, nil, false, nil
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.GetModelRouterAPIKeySpend(ctx, parsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, false, nil
		}
		return 0, nil, false, err
	}
	return row.SpentUsdMicros, row.SpendCapUsdMicros, true, nil
}

func (r *BillingRepo) GetUserMonthlySpendAndLimit(ctx context.Context, organizationID, routerUserID string) (int64, *int64, int64, error) {
	parsed, ok := parseOptionalUUID(routerUserID)
	if !ok {
		return 0, nil, 0, nil
	}
	q := dbbudget.Queries(r.tx)
	row, err := q.GetUserMonthlySpendAndLimit(ctx, sqlc.GetUserMonthlySpendAndLimitParams{
		RouterUserID:   parsed,
		OrganizationID: organizationID,
	})
	if err != nil {
		return 0, nil, 0, err
	}
	limit := row.OrgDefaultLimitUsdMicros
	if row.HasOverride {
		limit = row.OverrideLimitUsdMicros
	}
	return row.SpentUsdMicros, limit, row.GrantedUsdMicros, nil
}

func (r *BillingRepo) GetOrgMonthlySpendAndLimit(ctx context.Context, organizationID string) (int64, *int64, error) {
	q := dbbudget.Queries(r.tx)
	row, err := q.GetOrgMonthlySpendAndLimit(ctx, organizationID)
	if err != nil {
		return 0, nil, err
	}
	return row.SpentUsdMicros, row.OrgLimitUsdMicros, nil
}

func (r *BillingRepo) GetAutopayConfig(ctx context.Context, owner billing.Owner) (bool, int64, error) {
	if err := owner.Validate(); err != nil {
		return false, 0, err
	}
	q := dbbudget.Queries(r.tx)
	var enabled bool
	var threshold int64
	var err error
	switch owner.Kind {
	case billing.OwnerKindOrganization:
		row, queryErr := q.GetAutopayConfig(ctx, owner.OrganizationID)
		enabled, threshold, err = row.Enabled, row.ThresholdUsdMicros, queryErr
	default:
		return false, 0, billing.ErrInvalidOwner
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return enabled, threshold, nil
}

func (r *BillingRepo) BillingTablesExist(ctx context.Context) (bool, error) {
	q := dbbudget.Queries(r.tx)
	return q.CheckBillingTablesExist(ctx)
}

func parseOptionalUUID(value string) (uuid.UUID, bool) {
	parsed, err := uuid.Parse(value)
	return parsed, err == nil
}
