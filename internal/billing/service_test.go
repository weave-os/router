package billing_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogs swaps slog's default logger for one writing text lines into
// the returned buffer, restoring the previous default on test cleanup.
//
// observability.Get() lazily installs its own default handler exactly once
// (sync.Once) the first time any test calls it; if that happens after we've
// installed our buffer handler, it clobbers it. Force that one-time init
// first so our SetDefault below is the one that sticks.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	observability.Get()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// fakeRepo is an in-memory billing.Repo for testing the Service without
// hitting Postgres. Atomic fields keep the concurrent-debit test honest.
type fakeRepo struct {
	mu               sync.Mutex
	balanceMicros    int64
	hasOverride      bool
	balanceErr       error
	overrideErr      error
	debitErr         error
	ledgerCalls      []billing.DebitParams
	balanceRowExists bool
	debitCalls       atomic.Int32
	autopayEnabled   bool
	autopayThreshold int64
	autopayErr       error
	userMonthSpent   int64
	userMonthLimit   *int64
	userMonthErr     error
	orgMonthSpent    int64
	orgMonthLimit    *int64
	orgMonthErr      error
}

func (r *fakeRepo) GetBalance(_ context.Context, _ string) (int64, error) {
	if r.balanceErr != nil {
		return 0, r.balanceErr
	}
	if !r.balanceRowExists {
		return 0, billing.ErrBalanceRowMissing
	}
	return r.balanceMicros, nil
}

func (r *fakeRepo) HasActiveOverride(_ context.Context, _ string) (bool, error) {
	if r.overrideErr != nil {
		return false, r.overrideErr
	}
	return r.hasOverride, nil
}

func (r *fakeRepo) DebitInference(_ context.Context, p billing.DebitParams) (int64, error) {
	r.debitCalls.Add(1)
	if r.debitErr != nil {
		return 0, r.debitErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.balanceRowExists {
		return 0, billing.ErrBalanceRowMissing
	}
	// Mirrors the real repo: the balance moves by delta + fee, since the fee
	// row is written in the same statement.
	r.balanceMicros += p.DeltaUsdMicros + p.FeeUsdMicros
	r.ledgerCalls = append(r.ledgerCalls, p)
	return r.balanceMicros, nil
}

func (r *fakeRepo) BillingTablesExist(_ context.Context) (bool, error) { return true, nil }

func (r *fakeRepo) GetAPIKeySpend(_ context.Context, _ string) (int64, *int64, bool, error) {
	return 0, nil, false, nil
}

func (r *fakeRepo) GetUserMonthlySpendAndLimit(_ context.Context, _, _ string) (int64, *int64, error) {
	if r.userMonthErr != nil {
		return 0, nil, r.userMonthErr
	}
	return r.userMonthSpent, r.userMonthLimit, nil
}

func (r *fakeRepo) GetOrgMonthlySpendAndLimit(_ context.Context, _ string) (int64, *int64, error) {
	if r.orgMonthErr != nil {
		return 0, nil, r.orgMonthErr
	}
	return r.orgMonthSpent, r.orgMonthLimit, nil
}

func (r *fakeRepo) GetAutopayConfig(_ context.Context, _ string) (bool, int64, error) {
	if r.autopayErr != nil {
		return false, 0, r.autopayErr
	}
	return r.autopayEnabled, r.autopayThreshold, nil
}

// fakeAutopayNotifier records the org ids the service asked to recharge.
type fakeAutopayNotifier struct {
	mu       sync.Mutex
	notified []string
}

func (n *fakeAutopayNotifier) NotifyRechargeNeeded(organizationID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified = append(n.notified, organizationID)
}

func (n *fakeAutopayNotifier) calls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notified...)
}

func TestCheckBalance_Override(t *testing.T) {
	repo := &fakeRepo{hasOverride: true, balanceRowExists: true, balanceMicros: 5_000_000}
	svc := billing.NewService(repo)
	res, err := svc.CheckBalance(context.Background(), "org_x")
	require.NoError(t, err)
	assert.True(t, res.HasOverride)
	// Override path skips the balance read entirely; the row may be missing.
	assert.Equal(t, int64(0), res.BalanceMicros, "balance must be skipped on override path")
}

func TestCheckBalance_Healthy(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 9_500_000}
	svc := billing.NewService(repo)
	res, err := svc.CheckBalance(context.Background(), "org_x")
	require.NoError(t, err)
	assert.False(t, res.HasOverride)
	assert.Equal(t, int64(9_500_000), res.BalanceMicros)
}

func TestCheckBalance_MissingRowPropagates(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: false}
	svc := billing.NewService(repo)
	_, err := svc.CheckBalance(context.Background(), "org_x")
	assert.ErrorIs(t, err, billing.ErrBalanceRowMissing)
}

func TestDebitForInference_MatchesExportedCostMath(t *testing.T) {
	// Debit cost must match the OTel emitter/telemetry writer (same pricing
	// funcs) — drift means the billed amount diverges from the dashboard.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	balance, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_x",
		RouterRequestID: "req_abc",
		Model:           "claude-sonnet-4-5",
		Provider:        providers.ProviderAnthropic,
		InputTokens:     1_000_000,
		OutputTokens:    250_000,
		Pricing:         p,
	})
	require.NoError(t, err)

	// Expected: 1_000_000 fresh * $3/M + 250_000 * $15/M = 3.00 + 3.75 = 6.75
	expectedMicros := int64(6_750_000)
	assert.Equal(t, int64(10_000_000-expectedMicros), balance, "balance reflects raw upstream cost")
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, -expectedMicros, repo.ledgerCalls[0].DeltaUsdMicros, "delta is the negative charge")
	assert.Equal(t, expectedMicros, repo.ledgerCalls[0].NotionalCostMicros, "notional matches the charge")
	assert.Equal(t, billing.EntryTypeInference, repo.ledgerCalls[0].EntryType)
	assert.Equal(t, "req_abc", repo.ledgerCalls[0].RouterRequestID)
	assert.Equal(t, "claude-sonnet-4-5", repo.ledgerCalls[0].RouterModel)
}

func TestDebitForInference_ByokChargesFeeOnlyNotUpstreamCost(t *testing.T) {
	// The customer paid their own provider for the upstream call, so Weave
	// debits only its platform fee. The inference row still carries the full
	// notional cost so the savings dashboard can price the turn.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo).WithByokFeeRate(0.05)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	balance, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_byok",
		RouterRequestID: "req_byok",
		Model:           "claude-sonnet-4-5",
		Provider:        providers.ProviderAnthropic,
		InputTokens:     1_000_000,
		OutputTokens:    250_000,
		Pricing:         p,
		ByokServed:      true,
	})
	require.NoError(t, err)

	// Same turn as TestDebitForInference_MatchesExportedCostMath: $6.75 upstream.
	const notionalMicros = int64(6_750_000)
	const expectedFee = int64(337_500) // 5% of $6.75 = $0.3375

	require.Len(t, repo.ledgerCalls, 1, "the fee rides on the same atomic debit, not a second call")
	row := repo.ledgerCalls[0]
	assert.Equal(t, int64(0), row.DeltaUsdMicros,
		"a BYOK turn must not debit upstream cost — the customer already paid their provider")
	assert.Equal(t, notionalMicros, row.NotionalCostMicros,
		"notional cost is still recorded so the savings dashboard can price the turn")
	assert.Equal(t, billing.EntryTypeInference, row.EntryType)
	assert.Equal(t, -expectedFee, row.FeeUsdMicros, "fee is 5% of upstream cost, signed as a debit")
	assert.Equal(t, billing.EntryTypeByokFee, row.FeeEntryType)
	assert.Equal(t, int64(10_000_000)-expectedFee, balance,
		"balance drops by the fee alone")
}

func TestDebitForInference_ByokFeeIsExactlyFivePercent(t *testing.T) {
	// Guards the rate math: a refactor that changes rounding silently
	// re-prices every BYOK customer on a fee-charging deployment.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 1_000_000_000}
	svc := billing.NewService(repo).WithByokFeeRate(0.05)
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_byok",
		RouterRequestID: "req_round",
		Model:           "m",
		Provider:        providers.ProviderAnthropic,
		// $100.00 of input at $1/M — a round number so the 5% is unambiguous.
		InputTokens: 100_000_000,
		Pricing:     catalog.Pricing{InputUSDPer1M: 1.00},
		ByokServed:  true,
	})
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(100_000_000), repo.ledgerCalls[0].NotionalCostMicros, "$100 upstream")
	assert.Equal(t, int64(-5_000_000), repo.ledgerCalls[0].FeeUsdMicros,
		"$100 of customer upstream spend must bill exactly $5")
}

func TestDebitForInference_ByokDefaultRateChargesNoFee(t *testing.T) {
	// The default BYOK fee rate is zero: a BYOK turn debits nothing and
	// writes no fee, only the $0 inference row with its notional trail.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	balance, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_byok",
		RouterRequestID: "req_free",
		Model:           "m",
		Provider:        providers.ProviderAnthropic,
		InputTokens:     1_000_000,
		Pricing:         catalog.Pricing{InputUSDPer1M: 3.00},
		ByokServed:      true,
	})
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	row := repo.ledgerCalls[0]
	assert.Equal(t, int64(0), row.DeltaUsdMicros)
	assert.Equal(t, int64(0), row.FeeUsdMicros, "zero rate must charge no fee")
	assert.Equal(t, int64(3_000_000), row.NotionalCostMicros)
	assert.Equal(t, int64(10_000_000), balance, "balance unchanged")
}

func TestDebitForInference_OverrideOutranksByok(t *testing.T) {
	// A free-credits/internal org must not be charged a BYOK fee.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo).WithByokFeeRate(0.05)
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_override",
		RouterRequestID: "req_ovr",
		Model:           "m",
		Provider:        providers.ProviderAnthropic,
		InputTokens:     1_000_000,
		Pricing:         catalog.Pricing{InputUSDPer1M: 3.00},
		HasOverride:     true,
		ByokServed:      true,
	})
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(0), repo.ledgerCalls[0].DeltaUsdMicros)
	assert.Equal(t, int64(0), repo.ledgerCalls[0].FeeUsdMicros,
		"an override org pays no BYOK fee")
}

func TestDebitForInference_NonByokWritesNoFeeRow(t *testing.T) {
	// Regression guard: the fee params must stay zero on the ordinary
	// platform-key path, or every managed turn would grow a spurious fee row.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_plain",
		RouterRequestID: "req_plain",
		Model:           "m",
		Provider:        providers.ProviderAnthropic,
		InputTokens:     1_000_000,
		Pricing:         catalog.Pricing{InputUSDPer1M: 3.00},
	})
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(-3_000_000), repo.ledgerCalls[0].DeltaUsdMicros, "full cost debited as usual")
	assert.Equal(t, int64(0), repo.ledgerCalls[0].FeeUsdMicros, "no fee on a platform-key turn")
}

func TestDebitForInference_WarnsOnZeroPricingForRealUsage(t *testing.T) {
	// A model ID with no catalog.Models entry resolves to a zero-value
	// Pricing (see catalog.PrimaryPriceFor). Debiting real token usage at
	// that price silently charges $0 — this must surface as an Error log so
	// the gap gets noticed instead of masked (finding [30]).
	buf := captureLogs(t)
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	balance, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_x",
		RouterRequestID: "req_unknown",
		Model:           "gpt-5.2",
		Provider:        providers.ProviderOpenAI,
		InputTokens:     1_000_000,
		OutputTokens:    250_000,
		Pricing:         catalog.Pricing{}, // zero value: model not in catalog
	})
	require.NoError(t, err)
	assert.Equal(t, int64(10_000_000), balance, "zero pricing charges nothing even though real tokens were used")
	assert.Contains(t, buf.String(), "level=ERROR")
	assert.Contains(t, buf.String(), "zero-value catalog pricing")
	assert.Contains(t, buf.String(), "gpt-5.2")
}

func TestDebitForInference_WarnsOnZeroPricingForCacheOnlyUsage(t *testing.T) {
	// A turn can be all cache reads/writes with zero fresh InputTokens/
	// OutputTokens (e.g. a fully cache-hit prompt) — that's still real,
	// billable usage and must not be treated as "nothing happened."
	buf := captureLogs(t)
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_x",
		RouterRequestID: "req_cache_only",
		Model:           "gpt-5.2",
		Provider:        providers.ProviderOpenAI,
		CacheRead:       500_000,
		Pricing:         catalog.Pricing{}, // zero value: model not in catalog
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "level=ERROR")
	assert.Contains(t, buf.String(), "zero-value catalog pricing", "cache-only usage must still trip the unknown-pricing warning")
}

func TestDebitForInference_NoWarnOnKnownPricing(t *testing.T) {
	buf := captureLogs(t)
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_x",
		Model:          "claude-sonnet-4-5",
		Provider:       providers.ProviderAnthropic,
		InputTokens:    1_000_000,
		OutputTokens:   250_000,
		Pricing:        p,
	})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "zero-value catalog pricing", "a priced model must not trip the unknown-pricing warning")
}

func TestDebitForInference_NoWarnOnZeroPricingOverride(t *testing.T) {
	// Override/subscription-served turns are intentionally free — a $0 debit
	// there is expected behavior, not a pricing gap.
	buf := captureLogs(t)
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 0}
	svc := billing.NewService(repo)
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_internal",
		Model:          "gpt-5.2",
		Provider:       providers.ProviderOpenAI,
		InputTokens:    1_000_000,
		OutputTokens:   250_000,
		Pricing:        catalog.Pricing{},
		HasOverride:    true,
	})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "zero-value catalog pricing", "override turns are exempt from the unknown-pricing warning")
}

func TestDebitForInference_NoWarnOnZeroTokenUsage(t *testing.T) {
	// 0-token usage (e.g. a failed request before generation) has zero
	// pricing AND zero tokens — nothing was actually billed, so no warning.
	buf := captureLogs(t)
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 5_000_000}
	svc := billing.NewService(repo)
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_x",
		Model:          "gpt-5.2",
		Provider:       providers.ProviderOpenAI,
		Pricing:        catalog.Pricing{},
	})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "zero-value catalog pricing")
}

func TestDebitForInference_OverrideWritesZeroDeltaWithNotional(t *testing.T) {
	// Override: ledger records the would-be charge in notional_cost_micros
	// but leaves balance untouched — the shadow trail for capacity planning.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 0}
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	balance, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_internal",
		Model:          "claude-sonnet-4-5",
		Provider:       providers.ProviderAnthropic,
		InputTokens:    1_000_000,
		OutputTokens:   250_000,
		Pricing:        p,
		HasOverride:    true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), balance, "override leaves balance unchanged")
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(0), repo.ledgerCalls[0].DeltaUsdMicros, "override delta must be zero")
	assert.Equal(t, int64(6_750_000), repo.ledgerCalls[0].NotionalCostMicros, "notional records would-be charge")
}

func TestDebitForInference_SubscriptionDebitsNothing(t *testing.T) {
	// Subscription-served: the customer's plan covers the tokens, so the debit
	// is 0 while notional still records the full would-be cost as a shadow trail.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	balance, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:     "org_sub",
		RouterRequestID:    "req_sub",
		Model:              "claude-opus-4-8",
		Provider:           providers.ProviderAnthropic,
		InputTokens:        1_000_000,
		OutputTokens:       250_000,
		Pricing:            p,
		SubscriptionServed: true,
	})
	require.NoError(t, err)

	assert.Equal(t, int64(10_000_000), balance, "subscription turns debit nothing")
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(0), repo.ledgerCalls[0].DeltaUsdMicros, "delta is zero — the customer's plan covers the tokens")
	assert.Equal(t, int64(6_750_000), repo.ledgerCalls[0].NotionalCostMicros, "notional still records the full would-be cost")
}

func TestDebitForInference_OverrideBeatsSubscription(t *testing.T) {
	// A comped/override org pays nothing even when the turn was subscription
	// served — override wins the precedence.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 5_000_000}
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	balance, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:     "org_internal",
		Model:              "claude-opus-4-8",
		Provider:           providers.ProviderAnthropic,
		InputTokens:        1_000_000,
		OutputTokens:       250_000,
		Pricing:            p,
		HasOverride:        true,
		SubscriptionServed: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5_000_000), balance, "override leaves balance unchanged even on a subscription turn")
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(0), repo.ledgerCalls[0].DeltaUsdMicros, "override delta is zero, beating the subscription fee")
	assert.Equal(t, int64(6_750_000), repo.ledgerCalls[0].NotionalCostMicros)
}

func TestDebitForInference_BalanceCanGoNegative(t *testing.T) {
	// Two concurrent debits against a thin balance: the Service has no
	// balance>=amount guard, so the second goes negative by design.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 500_000} // $0.50
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}

	for range 2 {
		_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
			OrganizationID: "org_x",
			InputTokens:    1_000_000,
			OutputTokens:   250_000,
			Pricing:        p,
			Provider:       providers.ProviderAnthropic,
		})
		require.NoError(t, err)
	}

	// 500_000 - 6_750_000*2 = -13_000_000
	assert.Equal(t, int64(-13_000_000), repo.balanceMicros)
	assert.Len(t, repo.ledgerCalls, 2, "both debits recorded; nothing dropped")
}

func TestDebitForInference_ZeroTokensYieldsZeroCharge(t *testing.T) {
	// 0-token usage (timeout/5xx before generation) must yield 0 notional
	// cost and leave the balance unchanged.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 5_000_000}
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_x",
		Pricing:        p,
		Provider:       providers.ProviderAnthropic,
	})
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(0), repo.ledgerCalls[0].DeltaUsdMicros)
	assert.Equal(t, int64(0), repo.ledgerCalls[0].NotionalCostMicros)
}

func TestDebitForInference_RepoErrorPropagates(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, debitErr: errors.New("conn refused")}
	svc := billing.NewService(repo)
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_x",
		Pricing:        catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00},
	})
	assert.Error(t, err)
}

func TestDebitForInference_AttributesAPIKey(t *testing.T) {
	// api_key_id and delta flow to the repo's CTE, which bumps the key's
	// lifetime spend by the debit magnitude (-delta).
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:  "org_x",
		RouterRequestID: "req_key",
		Model:           "claude-sonnet-4-5",
		Provider:        providers.ProviderAnthropic,
		InputTokens:     1_000_000,
		OutputTokens:    250_000,
		Pricing:         p,
		APIKeyID:        "key-123",
	})
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, "key-123", repo.ledgerCalls[0].APIKeyID, "api key id flows to the repo for per-key spend")
	// spent grows by -delta: a real debit's delta is negative, so the key's
	// lifetime spend rises by the full notional charge.
	assert.Equal(t, int64(6_750_000), -repo.ledgerCalls[0].DeltaUsdMicros, "per-key spend increment equals the charge")
}

func TestDebitForInference_SubscriptionLeavesKeySpendFlat(t *testing.T) {
	// A subscription-served turn debits 0, so the key's spend must not move —
	// the customer's own plan paid, not their Weave key budget.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID:     "org_sub",
		Model:              "claude-opus-4-8",
		Provider:           providers.ProviderAnthropic,
		InputTokens:        1_000_000,
		OutputTokens:       250_000,
		Pricing:            p,
		APIKeyID:           "key-sub",
		SubscriptionServed: true,
	})
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, "key-sub", repo.ledgerCalls[0].APIKeyID)
	assert.Equal(t, int64(0), repo.ledgerCalls[0].DeltaUsdMicros, "subscription turn debits 0, so key spend stays flat")
}

func TestDebitForInference_AutopaySignalsOnDownwardCrossing(t *testing.T) {
	// Each debit is $6.75 (1M input + 250k output at $3/$15 per 1M).
	p := catalog.Pricing{InputUSDPer1M: 3.00, OutputUSDPer1M: 15.00, CacheReadMultiplier: 0.10}
	debit := func(svc *billing.Service) error {
		_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
			OrganizationID: "org_x",
			InputTokens:    1_000_000,
			OutputTokens:   250_000,
			Pricing:        p,
			Provider:       providers.ProviderAnthropic,
		})
		return err
	}

	t.Run("fires once when the debit crosses below the threshold", func(t *testing.T) {
		// $10.00 balance, $5.00 threshold; one $6.75 debit lands at $3.25 (< $5).
		repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000, autopayEnabled: true, autopayThreshold: 5_000_000}
		notifier := &fakeAutopayNotifier{}
		svc := billing.NewService(repo).WithAutopayNotifier(notifier)
		require.NoError(t, debit(svc))
		assert.Equal(t, []string{"org_x"}, notifier.calls(), "the crossing debit signals exactly once")
	})

	t.Run("does not fire when already below the threshold (below to below)", func(t *testing.T) {
		// $3.25 balance is already under the $5 threshold: the next debit is not a crossing.
		repo := &fakeRepo{balanceRowExists: true, balanceMicros: 3_250_000, autopayEnabled: true, autopayThreshold: 5_000_000}
		notifier := &fakeAutopayNotifier{}
		svc := billing.NewService(repo).WithAutopayNotifier(notifier)
		require.NoError(t, debit(svc))
		assert.Empty(t, notifier.calls(), "below→below must not re-fire; that's the transition guard")
	})

	t.Run("does not fire when the debit stays above the threshold", func(t *testing.T) {
		// $100.00 → $93.25, still comfortably above $5.
		repo := &fakeRepo{balanceRowExists: true, balanceMicros: 100_000_000, autopayEnabled: true, autopayThreshold: 5_000_000}
		notifier := &fakeAutopayNotifier{}
		svc := billing.NewService(repo).WithAutopayNotifier(notifier)
		require.NoError(t, debit(svc))
		assert.Empty(t, notifier.calls())
	})

	t.Run("does not fire when autopay is disabled", func(t *testing.T) {
		repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000, autopayEnabled: false, autopayThreshold: 5_000_000}
		notifier := &fakeAutopayNotifier{}
		svc := billing.NewService(repo).WithAutopayNotifier(notifier)
		require.NoError(t, debit(svc))
		assert.Empty(t, notifier.calls())
	})

	t.Run("no notifier wired is a no-op", func(t *testing.T) {
		repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000, autopayEnabled: true, autopayThreshold: 5_000_000}
		svc := billing.NewService(repo) // autopay signalling not wired
		require.NoError(t, debit(svc))
	})
}

func TestHasOverrideFromContext_Default(t *testing.T) {
	assert.False(t, billing.HasOverrideFromContext(context.Background()))
}

func TestHasOverrideFromContext_True(t *testing.T) {
	ctx := context.WithValue(context.Background(), billing.HasOverrideContextKey, true)
	assert.True(t, billing.HasOverrideFromContext(ctx))
}

func TestDebitForInference_ByokReturnsPostFeeBalance(t *testing.T) {
	// The returned balance must be post-fee. The inference row records balance
	// before the fee CTE runs, so returning it would give a stale figure and
	// maybeSignalRecharge would miss threshold crossings on fee-only debits.
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 1_000_000}
	svc := billing.NewService(repo).WithByokFeeRate(0.05)
	balance, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_byok",
		InputTokens:    1_000_000,
		Pricing:        catalog.Pricing{InputUSDPer1M: 1.00},
		Provider:       providers.ProviderAnthropic,
		ByokServed:     true,
	})
	require.NoError(t, err)

	// $1.00 upstream → 5% fee = $0.05 = 50_000 micros.
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(-50_000), repo.ledgerCalls[0].FeeUsdMicros)
	assert.Equal(t, int64(950_000), balance,
		"returned balance must be post-fee (1_000_000 - 50_000), never the pre-fee 1_000_000")
}

func TestDebitForInference_ByokFeeCrossingSignalsAutopay(t *testing.T) {
	// End-to-end guard on the bug above: a BYOK turn whose FEE alone crosses the
	// autopay threshold must still signal. If the debit path reports a pre-fee
	// balance, this crossing is silently missed and the org never auto-recharges.
	repo := &fakeRepo{
		balanceRowExists: true,
		balanceMicros:    1_000_000,
		autopayEnabled:   true,
		autopayThreshold: 975_000,
	}
	notifier := &fakeAutopayNotifier{}
	svc := billing.NewService(repo).WithAutopayNotifier(notifier).WithByokFeeRate(0.05)

	_, err := svc.DebitForInference(context.Background(), billing.DebitInferenceParams{
		OrganizationID: "org_byok",
		InputTokens:    1_000_000,
		Pricing:        catalog.Pricing{InputUSDPer1M: 1.00},
		Provider:       providers.ProviderAnthropic,
		ByokServed:     true,
	})
	require.NoError(t, err)

	// Pre-debit 1_000_000 (>= 975_000), post-debit 950_000 (< 975_000) = a crossing.
	assert.Equal(t, []string{"org_byok"}, notifier.calls(),
		"a BYOK fee that crosses the autopay threshold must signal a recharge")
}
