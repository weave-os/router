package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/switchpolicy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const agreementSol = "gpt-6.1-sol"

func agreementFixture() (InsertTelemetryParams, turnLoopResult) {
	luna := catalog.ModelGPT6Luna
	solArm := "openai/" + agreementSol
	return InsertTelemetryParams{InstallationID: uuid.NewString(), RequestID: "synthetic-request", Timestamp: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
			SessionKey: []byte("synthetic-thread"), Role: sessionpin.DefaultRole, TurnType: string(turntype.ToolResult), UpstreamStatusCode: http.StatusOK,
			DecisionModel: luna, DecisionProvider: providers.ProviderOpenAI}, turnLoopResult{
			AuthoritativePerTurn: true, PinModel: luna, PinProvider: providers.ProviderOpenAI, PinPolicyGroup: string(escalation.Low),
			Decision: router.Decision{Model: luna, Provider: providers.ProviderOpenAI},
			Fresh: router.Decision{Model: agreementSol, Provider: providers.ProviderOpenAI, Metadata: &router.RoutingMetadata{
				Strategy: string(router.StrategyHMM), ClassifierArtifactSHA256: "synthetic-classifier", SelectionPolicySHA256: "synthetic-policy", RosterVersion: "synthetic-roster",
				ClassifierPredictedLabel: string(escalation.High), ClassifierProbabilities: map[string]float64{string(escalation.High): .9},
				CandidateProviders: map[string]string{agreementSol: providers.ProviderOpenAI, luna: providers.ProviderOpenAI},
				SelectionTrace: &router.SelectionTrace{SelectedGroup: string(escalation.High), SelectedArm: solArm + ":high", CandidateRosterIDs: []string{solArm, "openai/" + luna},
					EffectiveOrders: map[string][]string{string(escalation.High): {solArm + ":high"}}},
			}},
		}
}

func agreementContext(target policyregistry.ServingTarget, enabled bool) context.Context {
	ctx := requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{Target: string(target), StateNamespace: "synthetic-binding", ReleaseID: "synthetic-release", BindingID: "synthetic-binding"})
	epoch := 0
	if enabled {
		epoch = 1
	}
	return flags.WithOverrides(ctx, flags.Overrides{Ints: map[flags.Key]int{flags.KeyAgreementShadowEpoch: epoch}})
}

func TestAgreementShadowOptInAndInternalAdmission(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"no admission", flags.WithOverrides(context.Background(), flags.Overrides{Ints: map[flags.Key]int{flags.KeyAgreementShadowEpoch: 1}}), false},
		{"default disabled", requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{Target: string(policyregistry.TargetInternal)}), false},
		{"disabled override", agreementContext(policyregistry.TargetInternal, false), false},
		{"stable even if opted in", agreementContext(policyregistry.TargetStable, true), false},
		{"internal opted in", agreementContext(policyregistry.TargetInternal, true), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, res := agreementFixture()
			before := res
			applyAgreementShadowTelemetry(test.ctx, &p, res)
			if (p.agreementShadow != nil) != test.want {
				t.Fatalf("unexpected admission: %+v", p.agreementShadow)
			}
			if !reflect.DeepEqual(before, res) {
				t.Fatal("observation changed the live routing result")
			}
		})
	}
}

func TestAgreementShadowUsesRawEligibleProposalAndResolvedBinding(t *testing.T) {
	p, res := agreementFixture()
	md := res.Fresh.Metadata
	md.SelectionTrace.OverrideReason = router.SelectionOverrideSlidingThreshold
	md.SelectionTrace.SelectedGroup = string(escalation.Medium)
	res.Fresh.Model = catalog.ModelGPT6Luna
	md.SelectionTrace.SelectedArm = "openai/" + catalog.ModelGPT6Luna
	md.SelectionTrace.EffectiveOrders[string(escalation.High)] = []string{"openai/gpt-6-astra:high", "openai/" + agreementSol + ":high"}
	applyAgreementShadowTelemetry(agreementContext(policyregistry.TargetInternal, true), &p, res)
	if p.agreementShadow == nil || p.agreementShadow.Observation.Gap != switchpolicy.ShadowGapNone || p.agreementShadow.Observation.Proposal.Target.Model != agreementSol {
		t.Fatalf("serving sliding decision contaminated the raw proposal: %+v", p.agreementShadow)
	}
	if string(p.agreementShadow.Key.SessionKey) == string(p.SessionKey) {
		t.Fatal("shadow must use the admission-scoped state namespace")
	}
	md.CandidateProviders[agreementSol] = providers.ProviderOpenAIGateway
	p, _ = agreementFixture()
	applyAgreementShadowTelemetry(agreementContext(policyregistry.TargetInternal, true), &p, res)
	if p.agreementShadow.Observation.Proposal.Target.Provider != providers.ProviderOpenAIGateway {
		t.Fatal("shadow guessed the native provider instead of using the resolved gateway binding")
	}
}

func TestAgreementShadowResumptionUsesNewEpochNamespace(t *testing.T) {
	first, res := agreementFixture()
	ctx := agreementContext(policyregistry.TargetInternal, true)
	applyAgreementShadowTelemetry(ctx, &first, res)
	disabled := first
	disabled.agreementShadow = nil
	applyAgreementShadowTelemetry(flags.WithOverrides(ctx, flags.Overrides{Ints: map[flags.Key]int{flags.KeyAgreementShadowEpoch: 0}}), &disabled, res)
	if disabled.agreementShadow != nil {
		t.Fatal("disabled interval still created an observation")
	}
	resumed := first
	resumed.agreementShadow = nil
	applyAgreementShadowTelemetry(flags.WithOverrides(ctx, flags.Overrides{Ints: map[flags.Key]int{flags.KeyAgreementShadowEpoch: 2}}), &resumed, res)
	if resumed.agreementShadow == nil || resumed.agreementShadow.Epoch != 2 || reflect.DeepEqual(first.agreementShadow.Key.SessionKey, resumed.agreementShadow.Key.SessionKey) {
		t.Fatal("new activation reused old votes or a late callback namespace")
	}
}

func TestAgreementShadowUnsupportedAndMissingInputsBreakSegments(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*turnLoopResult)
		gap   switchpolicy.ShadowGap
	}{
		{"manual pin", func(r *turnLoopResult) { r.HardPinned = true }, switchpolicy.ShadowGapUnsupported},
		{"unavailable manual pin", func(r *turnLoopResult) { r.ForcedPinDropped = true }, switchpolicy.ShadowGapUnsupported},
		{"deadline fallback", func(r *turnLoopResult) { r.PolicyFallback = true }, switchpolicy.ShadowGapUnsupported},
		{"no fresh classifier", func(r *turnLoopResult) { r.Fresh = router.Decision{} }, switchpolicy.ShadowGapUnsupported},
		{"missing trace", func(r *turnLoopResult) { r.Fresh.Metadata.SelectionTrace = nil }, switchpolicy.ShadowGapMissing},
		{"missing artifact", func(r *turnLoopResult) { r.Fresh.Metadata.ClassifierArtifactSHA256 = "" }, switchpolicy.ShadowGapMissing},
		{"explicit floor", func(r *turnLoopResult) { r.Fresh.Metadata.SelectionTrace.ForcedGroup = string(escalation.Maximum) }, switchpolicy.ShadowGapUnsupported},
		{"unchanged cluster allowlist", func(r *turnLoopResult) { r.Fresh.Metadata.SelectionTrace.ClusterOverridesPresent = true }, switchpolicy.ShadowGapUnsupported},
		{"unreconstructable selection", func(r *turnLoopResult) { r.Fresh.Model = catalog.ModelGPT6Luna }, switchpolicy.ShadowGapMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, res := agreementFixture()
			test.alter(&res)
			applyAgreementShadowTelemetry(agreementContext(policyregistry.TargetInternal, true), &p, res)
			if p.agreementShadow == nil || p.agreementShadow.Observation.Gap != test.gap {
				t.Fatalf("boundary was silently scored: %+v", p.agreementShadow)
			}
		})
	}
}

type agreementTelemetry struct {
	*auxTelemetryRepo
	shadowMu sync.Mutex
	state    switchpolicy.ShadowSnapshot
	found    bool
	fail     bool
	notify   chan struct{}
}

func (r *agreementTelemetry) GetAgreementShadow(context.Context, switchpolicy.ShadowKey) (switchpolicy.ShadowSnapshot, bool, error) {
	r.shadowMu.Lock()
	defer r.shadowMu.Unlock()
	if r.fail {
		return switchpolicy.ShadowSnapshot{}, false, errors.New("synthetic store failure")
	}
	return r.state, r.found, nil
}

func (r *agreementTelemetry) CompareAndSwapAgreementShadow(_ context.Context, _ switchpolicy.ShadowKey, expected int64, next switchpolicy.ShadowSnapshot) (bool, error) {
	r.shadowMu.Lock()
	defer r.shadowMu.Unlock()
	if r.state.Revision != expected {
		return false, nil
	}
	r.state = next
	r.found = true
	if r.notify != nil {
		select {
		case r.notify <- struct{}{}:
		default:
		}
	}
	return true, nil
}

func TestAgreementShadowRecordsOnlyAfterBaselineTelemetryAndHandlesFailures(t *testing.T) {
	store := &agreementTelemetry{auxTelemetryRepo: &auxTelemetryRepo{}, notify: make(chan struct{}, 1)}
	svc := &Service{telemetry: store}
	p, res := agreementFixture()
	applyAgreementShadowTelemetry(agreementContext(policyregistry.TargetInternal, true), &p, res)
	svc.fireTelemetry(p)
	select {
	case <-store.notify:
	case <-time.After(3 * time.Second):
		t.Fatal("shadow observation not recorded")
	}
	if len(store.snapshot()) != 1 {
		t.Fatal("shadow advanced before baseline response telemetry")
	}
	store.shadowMu.Lock()
	state := store.state
	store.shadowMu.Unlock()
	if state.OneUp.Current.Target.Model != agreementSol || state.TwoUp.Current.Target.Model != catalog.ModelGPT6Luna {
		t.Fatalf("variants did not receive independent histories: %+v", state)
	}
	p.RequestID = "failed-request"
	p.Timestamp = p.Timestamp.Add(time.Second)
	p.UpstreamStatusCode = http.StatusBadGateway
	p.agreementShadow.Observation.RequestID = p.RequestID
	p.agreementShadow.Observation.StartedAt = p.Timestamp
	svc.recordAgreementShadow(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), p)
	store.shadowMu.Lock()
	defer store.shadowMu.Unlock()
	if store.state.OneUp.Current.Target != (switchpolicy.Target{}) || store.state.TwoUp.Current.Target != (switchpolicy.Target{}) {
		t.Fatal("failed inference was counted as agreement evidence")
	}
}

func TestAgreementShadowStoreErrorDoesNotLoseBaselineTelemetry(t *testing.T) {
	store := &agreementTelemetry{auxTelemetryRepo: &auxTelemetryRepo{}, fail: true}
	svc := &Service{telemetry: store}
	p, res := agreementFixture()
	applyAgreementShadowTelemetry(agreementContext(policyregistry.TargetInternal, true), &p, res)
	svc.fireTelemetry(p)
	rows := store.waitForRows(1)
	if len(rows) != 1 || rows[0].DecisionModel != p.DecisionModel || rows[0].UpstreamStatusCode != http.StatusOK {
		t.Fatalf("shadow error affected serving telemetry: %+v", rows)
	}
}

type agreementFailureRouter struct{}

func (agreementFailureRouter) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{}, errors.New("synthetic routing refusal")
}

func TestAgreementShadowRoutingRefusalBreaksHistoryWithoutDispatch(t *testing.T) {
	store := &agreementTelemetry{auxTelemetryRepo: &auxTelemetryRepo{}, notify: make(chan struct{}, 1)}
	pins := newStubPinStore()
	svc := NewService(agreementFailureRouter{}, nil, nil, false, nil, pins, false, providers.ProviderOpenAI, catalog.ModelGPT6Luna, store).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: agreementFailureRouter{}, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
	env, err := translate.ParseAnthropic([]byte(`{"model":"weave-auto","messages":[{"role":"user","content":"synthetic routing test"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := router.WithStrategy(agreementContext(policyregistry.TargetInternal, true), router.StrategyHMM)
	ctx = observability.WithRequestID(ctx, "synthetic-refusal")
	_, err = svc.runTurnLoop(ctx, env, env.RoutingFeatures(false), "synthetic-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: string(router.ModelAutomatic)})
	if err == nil {
		t.Fatal("synthetic refusal unexpectedly succeeded")
	}
	select {
	case <-store.notify:
	case <-time.After(3 * time.Second):
		t.Fatal("routing refusal did not invalidate the shadow history")
	}
	store.shadowMu.Lock()
	defer store.shadowMu.Unlock()
	if store.state.LastRequestID != "synthetic-refusal" || store.state.OneUp.Current.Target != (switchpolicy.Target{}) || store.state.TwoUp.Current.Target != (switchpolicy.Target{}) {
		t.Fatalf("routing refusal advanced a hypothetical choice: %+v", store.state)
	}
	if len(pins.upserts) != 0 {
		t.Fatal("shadow routing refusal wrote a live model pin")
	}
}
