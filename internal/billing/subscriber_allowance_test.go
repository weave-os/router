package billing_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSettler records the settlements the billing service books against an
// individual subscriber's allowance.
type fakeSettler struct {
	mu          sync.Mutex
	settlements []entitlement.Settlement
	err         error
}

func (f *fakeSettler) Settle(_ context.Context, settlement entitlement.Settlement) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settlements = append(f.settlements, settlement)
	return f.err
}

func (f *fakeSettler) recorded() []entitlement.Settlement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]entitlement.Settlement(nil), f.settlements...)
}

func coveredContext() context.Context {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return entitlement.WithCoverage(context.Background(), entitlement.Coverage{
		SubscriberID:       "cs_max",
		EntitlementVersion: 7,
		Plan:               entitlement.PlanMax,
		BillingPeriod: entitlement.Period{
			Kind:  entitlement.PeriodKindBilling,
			Start: start,
			End:   start.AddDate(0, 1, 0),
		},
		SixHourPeriod:         entitlement.SixHourWindowAt(start),
		BillingLimitUsdMicros: 50_000_000,
		SixHourLimitUsdMicros: 5_000_000,
	})
}

func subscriberParams() billing.DebitInferenceParams {
	return billing.DebitInferenceParams{
		OrganizationID:  "org_individual",
		RouterRequestID: "req_sub",
		Model:           "claude-sonnet-4-5",
		Provider:        providers.ProviderAnthropic,
		InputTokens:     1_000_000,
		Pricing:         catalog.Pricing{InputUSDPer1M: 3.00},
		APIKeyID:        "key_1",
	}
}

func TestDebitForInferenceMetersCoveredTurnInsteadOfDebitingOrg(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	settler := &fakeSettler{}
	svc := billing.NewService(repo).WithSubscriberAllowance(settler)

	balance, err := svc.DebitForInference(coveredContext(), subscriberParams())
	require.NoError(t, err)
	assert.Equal(t, int64(10_000_000), balance, "a covered turn leaves the org balance untouched")

	require.Len(t, repo.ledgerCalls, 1)
	assert.Zero(t, repo.ledgerCalls[0].DeltaUsdMicros)
	assert.Equal(t, int64(3_000_000), repo.ledgerCalls[0].NotionalCostMicros, "notional cost is still recorded for reporting")

	settlements := settler.recorded()
	require.Len(t, settlements, 1)
	assert.Equal(t, entitlement.SubscriberID("cs_max"), settlements[0].Coverage.SubscriberID)
	assert.Equal(t, int64(3_000_000), settlements[0].RetailUsdMicros)
	assert.Equal(t, entitlement.CapacitySourceIncludedRouter, settlements[0].CapacitySource)
	assert.Equal(t, "req_sub", settlements[0].RouterRequestID)
	assert.Equal(t, "key_1", settlements[0].APIKeyID)
}

func TestDebitForInferenceLeavesEnterpriseTurnUnchanged(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	settler := &fakeSettler{}
	svc := billing.NewService(repo).WithSubscriberAllowance(settler)

	balance, err := svc.DebitForInference(context.Background(), subscriberParams())
	require.NoError(t, err)
	assert.Equal(t, int64(7_000_000), balance, "a turn with no coverage still debits the org balance")
	assert.Empty(t, settler.recorded())
}

func TestDebitForInferenceDoesNotMeterTurnsThePlanDidNotPayFor(t *testing.T) {
	for name, mutate := range map[string]func(*billing.DebitInferenceParams){
		"subscription served": func(p *billing.DebitInferenceParams) { p.SubscriptionServed = true },
		"byok served":         func(p *billing.DebitInferenceParams) { p.ByokServed = true },
		"billing override":    func(p *billing.DebitInferenceParams) { p.HasOverride = true },
	} {
		t.Run(name, func(t *testing.T) {
			repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
			settler := &fakeSettler{}
			svc := billing.NewService(repo).WithSubscriberAllowance(settler)
			p := subscriberParams()
			mutate(&p)

			_, err := svc.DebitForInference(coveredContext(), p)
			require.NoError(t, err)
			assert.Empty(t, settler.recorded(), "the included allowance only pays for turns Weave itself bought")
		})
	}
}

func TestDebitForInferenceChargesTheOrgWhenSettlementFails(t *testing.T) {
	buf := captureLogs(t)
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	settler := &fakeSettler{err: errors.New("allowance write failed")}
	svc := billing.NewService(repo).WithSubscriberAllowance(settler)

	balance, err := svc.DebitForInference(coveredContext(), subscriberParams())
	require.NoError(t, err, "the turn was already served; a metering failure must not surface as a billing error")
	assert.Equal(t, int64(7_000_000), balance,
		"a turn the allowance could not record is charged to the org rather than served free and unmetered")
	require.Len(t, repo.ledgerCalls, 1)
	assert.Equal(t, int64(-3_000_000), repo.ledgerCalls[0].DeltaUsdMicros)
	assert.Contains(t, buf.String(), "level=ERROR")
}

func TestDebitForInferenceLeavesTheOrgAloneWhenTheHoldStands(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	settler := &fakeSettler{err: fmt.Errorf("settle: %w", entitlement.ErrAllowanceHeldUnsettled)}
	svc := billing.NewService(repo).WithSubscriberAllowance(settler)

	_, err := svc.DebitForInference(coveredContext(), subscriberParams())
	require.NoError(t, err)
	require.Len(t, repo.ledgerCalls, 1)
	assert.Zero(t, repo.ledgerCalls[0].DeltaUsdMicros,
		"a hold that stands already draws the windows down, so the org must not be charged as well")
}

func TestDebitForInferenceKeepsRequestedAndServedModelApart(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	settler := &fakeSettler{}
	svc := billing.NewService(repo).WithSubscriberAllowance(settler)
	p := subscriberParams()
	p.RequestedModel = "claude-opus-4-5"

	_, err := svc.DebitForInference(coveredContext(), p)
	require.NoError(t, err)

	settlements := settler.recorded()
	require.Len(t, settlements, 1)
	assert.Equal(t, "claude-opus-4-5", settlements[0].RequestedModel, "failover must not rewrite what the client asked for")
	assert.Equal(t, "claude-sonnet-4-5", settlements[0].ServedModel)
}

func TestDebitForInferenceGivesEachActionOfARequestItsOwnIdentity(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	settler := &fakeSettler{}
	svc := billing.NewService(repo).WithSubscriberAllowance(settler)
	ctx := coveredContext()

	// A handover summary or compaction turn bills a second time under the
	// same router request; both must land as distinct allowance actions.
	_, err := svc.DebitForInference(ctx, subscriberParams())
	require.NoError(t, err)
	_, err = svc.DebitForInference(ctx, subscriberParams())
	require.NoError(t, err)

	settlements := settler.recorded()
	require.Len(t, settlements, 2)
	assert.NotEqual(t, settlements[0].ActionID, settlements[1].ActionID)
}

func TestDebitForInferenceIgnoresCoverageWithoutASettler(t *testing.T) {
	repo := &fakeRepo{balanceRowExists: true, balanceMicros: 10_000_000}
	svc := billing.NewService(repo)

	balance, err := svc.DebitForInference(coveredContext(), subscriberParams())
	require.NoError(t, err)
	assert.Equal(t, int64(7_000_000), balance, "without metering wired, a covered turn must not become free")
}
