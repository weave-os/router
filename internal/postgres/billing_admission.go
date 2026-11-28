package postgres

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

func (r *BillingRepo) ReadAdmission(ctx context.Context, scope billing.AdmissionScope) (billing.Admission, error) {
	key, ok := parseOptionalUUID(scope.KeyID)
	p := sqlc.GetBillingAdmissionParams{OrgID: scope.OrganizationID, KeyID: pgtype.UUID{Bytes: key, Valid: ok}, SessionID: scope.SessionID, TaskID: scope.TaskID}
	var delegatedErr error
	if scope.Delegated {
		var issuer uuid.UUID
		issuer, delegatedErr = uuid.Parse(scope.IssuerID)
		p.IssuerID = pgtype.UUID{Bytes: issuer, Valid: delegatedErr == nil}
	}
	row, err := dbbudget.Queries(r.tx).GetBillingAdmission(ctx, p)
	if err != nil {
		return billing.Admission{}, err
	}
	return billing.Admission{Balance: billing.CheckResult{HasOverride: row.HasOverride, BalanceMicros: row.Balance}, BalanceFound: row.HasBalance, Key: billing.APIKeySpendCapResult{Found: row.HasKey, SpentMicros: row.KeySpent, CapMicros: row.KeyCap}, Monthly: billing.MonthlySpendResult{SpentMicros: row.OrgSpent, LimitMicros: row.OrgLimit}, SessionSpent: row.SessionSpent, TaskSpent: row.TaskSpent, DelegatedErr: delegatedErr}, nil
}
