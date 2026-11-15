package billing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func byokLedgerRow(t *testing.T, feeRate float64, inputTokens int) (billing.DebitParams, int64) {
	t.Helper()
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 1_000_000}
	balance, err := billing.NewService(repo).WithByokFeeRate(feeRate).DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_byok",
		Model:          "m",
		Provider:       providers.ProviderAnthropic,
		InputTokens:    inputTokens,
		Pricing:        catalog.Pricing{InputUSDPer1M: 1.00},
		ByokServed:     true,
	})
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	return repo.ledgerCalls[0], balance
}

func TestByokFeeRoundsHalfAMicroUp(t *testing.T) {
	half, _ := byokLedgerRow(t, 0.05, 10)
	assert.Equal(t, int64(10), half.NotionalCostMicros)
	assert.Equal(t, int64(-1), half.FeeUsdMicros, "a fee of exactly half a micro rounds up to one micro")

	below, _ := byokLedgerRow(t, 0.05, 9)
	assert.Equal(t, int64(9), below.NotionalCostMicros)
	assert.Equal(t, int64(0), below.FeeUsdMicros, "a fee under half a micro rounds down to nothing")
}

func TestNegativeByokFeeRateChargesNoFee(t *testing.T) {
	row, balance := byokLedgerRow(t, -0.05, 1_000_000)
	assert.Equal(t, int64(0), row.FeeUsdMicros, "a negative fee rate clamps to zero instead of crediting the organization")
	assert.Equal(t, int64(1_000_000), balance)
}

func TestOverrideDebitReturnsTheUnchangedBalance(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 7_000_000}
	balance, err := billing.NewService(repo).DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_override",
		Model:          "m",
		Provider:       providers.ProviderAnthropic,
		InputTokens:    1_000_000,
		Pricing:        catalog.Pricing{InputUSDPer1M: 3.00},
		HasOverride:    true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(7_000_000), balance, "an override debit reports the balance it left untouched, not zero")
}

func TestAutopayConfigReadFailureNeverFailsTheDebit(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000, autopayErr: errors.New("config read failed")}
	notifier := &fakeAutopayNotifier{}
	balance, err := billing.NewService(repo).WithAutopayNotifier(notifier).DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_x",
		Model:          "m",
		Provider:       providers.ProviderAnthropic,
		InputTokens:    1_000_000,
		Pricing:        catalog.Pricing{InputUSDPer1M: 3.00},
	})
	require.NoError(t, err, "the debit already committed; a missed recharge signal is backstopped by the control plane's reconciliation sweep")
	assert.Equal(t, int64(7_000_000), balance)
	assert.Len(t, repo.autopayConfigOwners(), 1)
	assert.Empty(t, notifier.calls())
}

func TestDelegatedSpendCheckFailsWithoutAReader(t *testing.T) {
	_, _, err := billing.NewService(&fakeRepo{}).CheckDelegatedSpend(context.Background(), "key", "session", "task")
	assert.ErrorIs(t, err, billing.ErrDelegatedSpendUnavailable)
}

var ledgerEntryTypeCheck = regexp.MustCompile(`organization_credit_ledger_entry_type_check\s+CHECK\s*\(entry_type IN \(([^)]*)\)\)`)

func TestLedgerEntryTypesMatchTheLatestCheckConstraint(t *testing.T) {
	migrations, err := filepath.Glob(filepath.Join("..", "..", "db", "migrations", "*.up.sql"))
	require.NoError(t, err)
	var allowed string
	for _, migration := range migrations {
		source, err := os.ReadFile(migration)
		require.NoError(t, err)
		if match := ledgerEntryTypeCheck.FindSubmatch(source); match != nil {
			allowed = string(match[1])
		}
	}
	require.NotEmpty(t, allowed, "no migration defines router.organization_credit_ledger's entry_type CHECK constraint")
	for _, entryType := range []string{billing.EntryTypeInference, billing.EntryTypeByokFee} {
		assert.Contains(t, allowed, "'"+entryType+"'",
			"entry_type %q must be allowed by the latest organization_credit_ledger CHECK constraint (%s)", entryType, allowed)
	}
}
