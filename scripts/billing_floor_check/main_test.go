// Run against a disposable database with all router migrations applied:
//
//	ROUTER_TEST_DATABASE_URL=... go test ./scripts/billing_floor_check -count=1
package billing_floor_check

import (
	"context"
	"os"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/postgres"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRejectedFloorDebitKeepsAccountingTrail(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROUTER_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close(context.Background())) })

	organizationID := "org_" + uuid.NewString()[:32]
	requestID := "floor_" + uuid.NewString()
	const balanceBefore int64 = 100
	charge := billing.OrganizationBalanceFloorMicros - balanceBefore - 1
	_, err = connection.Exec(ctx, `
		INSERT INTO router.organization_credit_balance (organization_id, balance_usd_micros)
		VALUES ($1, $2)`, organizationID, balanceBefore)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = connection.Exec(context.Background(), `DELETE FROM router.organization_monthly_spend WHERE organization_id = $1`, organizationID)
		_, _ = connection.Exec(context.Background(), `DELETE FROM router.organization_credit_ledger WHERE organization_id = $1`, organizationID)
		_, _ = connection.Exec(context.Background(), `DELETE FROM router.organization_credit_balance WHERE organization_id = $1`, organizationID)
	})

	repository := postgres.NewBillingRepo(connection)
	balanceAfter, debitErr := repository.DebitInference(ctx, billing.DebitParams{
		OrganizationID:     organizationID,
		DeltaUsdMicros:     charge,
		NotionalCostMicros: -charge,
		EntryType:          billing.EntryTypeInference,
		RouterRequestID:    requestID,
		RouterModel:        "floor-test-model",
	})
	require.ErrorIs(t, debitErr, billing.ErrBalanceFloorExceeded)
	assert.Equal(t, balanceBefore, balanceAfter)

	var storedBalance int64
	require.NoError(t, connection.QueryRow(ctx, `
		SELECT balance_usd_micros FROM router.organization_credit_balance
		WHERE organization_id = $1`, organizationID).Scan(&storedBalance))
	assert.Equal(t, balanceBefore, storedBalance)

	var delta, notional, ledgerBalance int64
	var entryType string
	require.NoError(t, connection.QueryRow(ctx, `
		SELECT delta_usd_micros, notional_cost_micros, balance_after_micros, entry_type
		FROM router.organization_credit_ledger
		WHERE organization_id = $1 AND router_request_id = $2`, organizationID, requestID).
		Scan(&delta, &notional, &ledgerBalance, &entryType))
	assert.Equal(t, charge, delta)
	assert.Equal(t, -charge, notional)
	assert.Equal(t, balanceBefore, ledgerBalance)
	assert.Equal(t, billing.EntryTypeUnfunded, entryType)

	var spent int64
	require.NoError(t, connection.QueryRow(ctx, `
		SELECT spent_usd_micros FROM router.organization_monthly_spend
		WHERE organization_id = $1
		  AND month = DATE_TRUNC('month', NOW() AT TIME ZONE 'utc')::date`, organizationID).Scan(&spent))
	assert.Equal(t, -charge, spent)
}
