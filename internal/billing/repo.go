package billing

import "context"

type Repo interface {
	GetBalance(ctx context.Context, orgID string) (balanceMicros int64, err error)

	HasActiveOverride(ctx context.Context, orgID string) (bool, error)

	DebitInference(ctx context.Context, p DebitParams) (balanceAfterMicros int64, err error)

	GetAPIKeySpend(ctx context.Context, apiKeyID string) (spentMicros int64, capMicros *int64, found bool, err error)

	GetUserMonthlySpendAndLimit(ctx context.Context, organizationID, routerUserID string) (spentMicros int64, limitMicros *int64, err error)

	GetOrgMonthlySpendAndLimit(ctx context.Context, organizationID string) (spentMicros int64, limitMicros *int64, err error)

	GetAutopayConfig(ctx context.Context, owner Owner) (enabled bool, thresholdMicros int64, err error)

	BillingTablesExist(ctx context.Context) (bool, error)
}

type DebitParams struct {
	OrganizationID     string
	DeltaUsdMicros     int64
	NotionalCostMicros int64
	EntryType          string
	FeeUsdMicros       int64
	FeeEntryType       string
	RouterRequestID    string
	RouterModel        string
	APIKeyID           string
	DelegatedSessionID string
	DelegatedTaskID    string
	RouterUserID       string
}

type DelegatedSpendReader interface {
	GetDelegatedTokenSpend(ctx context.Context, issuerKeyID, sessionID, taskID string) (sessionSpentMicros, taskSpentMicros int64, err error)
}
