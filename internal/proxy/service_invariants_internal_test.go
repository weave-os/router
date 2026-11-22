package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/routingpolicy"

	"github.com/google/uuid"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

func TestPinNeverExpires_OutlivesAnyIdleGapWithinPostgresRange(t *testing.T) {
	if !pinNeverExpires.After(time.Now().AddDate(1000, 0, 0)) {
		t.Fatalf("pinNeverExpires = %v, want at least 1000 years out", pinNeverExpires)
	}
	if pinNeverExpires.Year() > 9999 {
		t.Fatalf("pinNeverExpires = %v, want a four-digit year inside the Postgres timestamp range", pinNeverExpires)
	}
}

func TestRefusalObserver_CatchesSignalSplitDeepIntoPreviousChunk(t *testing.T) {
	signal := `"stop_reason":"refusal"`
	split := len(signal) - 3
	rec := httptest.NewRecorder()
	obs := newRefusalObserver(rec)
	if _, err := obs.Write([]byte(`data: {"type":"message_delta","delta":{` + strings.Repeat(" ", 256) + signal[:split])); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if obs.refused {
		t.Fatal("refusal detected before the signal completed")
	}
	if _, err := obs.Write([]byte(signal[split:] + `}}`)); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if !obs.refused {
		t.Fatal("refusal split across writes was not detected")
	}
}

type cancellationCheckingBillingRepo struct {
	*auxBillingRepo
	ctxErrs []error
}

func (r *cancellationCheckingBillingRepo) DebitInference(ctx context.Context, p billing.DebitParams) (int64, error) {
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
	return r.auxBillingRepo.DebitInference(ctx, p)
}

func TestFireBilling_DebitsAfterClientCancellation(t *testing.T) {
	repo := &cancellationCheckingBillingRepo{auxBillingRepo: &auxBillingRepo{}}
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), billing: billing.NewService(repo)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.fireBilling(ctx, billing.DebitInferenceParams{
		OrganizationID:  auxTestOrgID,
		RouterRequestID: auxTestRequestID,
		Model:           auxTestModel,
		Provider:        "anthropic",
		InputTokens:     4000,
		OutputTokens:    700,
	})

	if len(repo.ctxErrs) != 1 {
		t.Fatalf("debits = %d, want 1", len(repo.ctxErrs))
	}
	if repo.ctxErrs[0] != nil {
		t.Fatalf("debit ran on a canceled context: %v", repo.ctxErrs[0])
	}
}

type cancellationCheckingPinStore struct {
	*repinFakeStore
	ctxErrs []error
}

func (f *cancellationCheckingPinStore) Upsert(ctx context.Context, p sessionpin.Pin) error {
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
	return f.repinFakeStore.Upsert(ctx, p)
}

func TestMaybeRepinOnRefusal_WritesAfterRequestCancellation(t *testing.T) {
	store := &cancellationCheckingPinStore{repinFakeStore: &repinFakeStore{}}
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store, rescueConfig: dispatch.RescueConfig{CyberRefusalRepin: true, CyberRefusalFallbackModel: "claude-sonnet-5"}}
	ctx, cancel := context.WithCancel(repinCtx())
	cancel()

	s.maybeRepinOnRefusal(ctx, router.StrategyFromContext(ctx), &refusalObserver{refused: true}, [sessionpin.SessionKeyLen]byte{7, 8, 9}, "main_loop",
		router.Decision{Provider: "anthropic", Model: "claude-opus-4-8"})

	if len(store.ctxErrs) != 1 {
		t.Fatalf("upserts = %d, want 1", len(store.ctxErrs))
	}
	if store.ctxErrs[0] != nil {
		t.Fatalf("re-pin ran on a canceled context: %v", store.ctxErrs[0])
	}
}

func TestIsDegenerateResponse_ExcludesDemotedStopReason(t *testing.T) {
	if !isDegenerateResponse(0, 0, "end_turn", false) {
		t.Fatal("short natural end_turn with no tools should be degenerate")
	}
	if isDegenerateResponse(0, 0, "end_turn", true) {
		t.Fatal("a cross-format demoted stop reason must not count as degenerate")
	}
	if isDegenerateResponse(0, 1, "end_turn", false) {
		t.Fatal("a tool-only turn must not count as degenerate")
	}
}

type cancellationCheckingRecoveryStore struct {
	recoveryLeaseStore
	ctxErrs []error
}

func (s *cancellationCheckingRecoveryStore) ClearDemotionCooldown(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role, model string, observedUntil time.Time) error {
	s.ctxErrs = append(s.ctxErrs, ctx.Err())
	return s.recoveryLeaseStore.ClearDemotionCooldown(ctx, key, role, model, observedUntil)
}

func TestClearRecoveredCooldown_LandsAfterRequestCancellation(t *testing.T) {
	store := &cancellationCheckingRecoveryStore{}
	s := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observedUntil := time.Now().Add(-time.Second)

	clearRecoveredCooldown(ctx, s, routingpolicy.TurnRoute{
		SessionKey:     [sessionpin.SessionKeyLen]byte{1},
		CooldownProbes: map[string]time.Time{"recovered-model": observedUntil},
	}, "recovered-model", nil)

	if len(store.ctxErrs) == 0 {
		t.Fatal("recovered cooldown was not cleared")
	}
	for _, err := range store.ctxErrs {
		if err != nil {
			t.Fatalf("cooldown clear ran on a canceled context: %v", err)
		}
	}
}

func TestCacheCreation1hPtr_NullWithoutCacheCreation(t *testing.T) {
	if got := cacheCreation1hPtr(0, 0); got != nil {
		t.Fatalf("cacheCreation1hPtr(0, 0) = %v, want nil", *got)
	}
	got := cacheCreation1hPtr(100, 0)
	if got == nil || *got != 0 {
		t.Fatalf("cacheCreation1hPtr(100, 0) = %v, want a measured zero", got)
	}
}

func TestRefusalCategory_RejectsOverlongValue(t *testing.T) {
	if got := refusalCategory([]byte(`"category":"` + strings.Repeat("a", 65) + `"`)); got != "" {
		t.Fatalf("refusalCategory accepted a %d-byte value", len(got))
	}
}

func TestRefusalObserver_CategoryBeforeOverlapWindowIsKept(t *testing.T) {
	obs := newRefusalObserver(httptest.NewRecorder())
	if _, err := obs.Write([]byte(`data: {"category":"cyber"` + strings.Repeat(" ", 4*refusalScanOverlap))); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if _, err := obs.Write([]byte(`,"stop_reason":"refusal"}`)); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if !obs.refused {
		t.Fatal("refusal not detected")
	}
	if obs.category != "cyber" {
		t.Fatalf("category = %q, want cyber from the earlier chunk", obs.category)
	}
}

func TestEmitBilling_HandoverSummaryDebitedOnceAlongsideTurn(t *testing.T) {
	cases := []struct {
		name     string
		handover handover.Outcome
		want     []string
	}{
		{"no handover", handover.Outcome{}, []string{auxTestRequestID}},
		{"summary served", handover.Outcome{Invoked: true, SummaryUsage: auxTestUsage()}, []string{auxTestRequestID, auxTestRequestID + auxSuffixHandoverSummary}},
		{"fell back to full history", handover.Outcome{Invoked: true, FallbackToFullHistory: true, SummaryUsage: auxTestUsage()}, []string{auxTestRequestID}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, repo, _ := auxTestService(t)
			s.emitBilling(auxTestContext(uuid.New()), auxTestRequestID, auxTestOrgID, auxTestModel,
				router.Decision{Provider: "anthropic", Model: auxTestModel}, catalog.Pricing{}, routingpolicy.TurnRoute{Handover: c.handover},
				4000, 700, 0, 0, catalog.UsageModifiers{})
			var got []string
			for _, d := range repo.snapshot() {
				got = append(got, d.RouterRequestID)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("debited request ids = %v, want %v", got, c.want)
			}
		})
	}
}

func TestUsageBypassEngaged_UnsetThresholdDefaultsToNinetyFivePercent(t *testing.T) {
	const token = "sk-ant-oat01-test-subscription-token"
	for _, c := range []struct {
		used float64
		want bool
	}{{0.90, true}, {0.96, false}} {
		obs := usage.NewObserver([]byte("salt"), 10*time.Minute, time.Now)
		obs.Record(obs.Key([]byte(token)), usage.Snapshot{Primary: usage.Window{UsedPercent: c.used, WindowMinutes: 300}})
		svc := &Service{strategyServing: hmmPorts(), engine: dispatch.NewEngine(dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: &bypassFakeProvider{}, providers.ProviderOpenAI: &bypassFakeProvider{}})), credentials: credentials.Brokerage{UsageObserver: obs}}
		ctx := context.WithValue(context.Background(), credentials.AnthropicSubscriptionContextKey{}, token)
		ctx = context.WithValue(ctx, requestcontext.InstallationUsageBypassContextKey{}, UsageBypassConfig{Enabled: true})
		_, ok := svc.usageBypassEngaged(ctx, http.Header{}, router.Request{
			RequestedModel:   "claude-sonnet-4-6",
			EnabledProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
		})
		if ok != c.want {
			t.Fatalf("used %.2f with no threshold chosen: engaged = %v, want %v", c.used, ok, c.want)
		}
	}
}
