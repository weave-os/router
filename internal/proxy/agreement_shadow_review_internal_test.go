package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/switchpolicy"
	"weave-os/router/internal/translate"
)

func TestAgreementShadowCompletedQualityLabelsKeepEvidence(t *testing.T) {
	for _, quality := range []TurnErrorClass{TurnErrorMaxTokens, TurnErrorRefusal, TurnErrorInvalidToolArgs} {
		t.Run(string(quality), func(t *testing.T) {
			store := &agreementTelemetry{auxTelemetryRepo: &auxTelemetryRepo{}}
			svc := &Service{telemetry: store}
			p, res := agreementFixture()
			ctx := agreementContext(policyregistry.TargetInternal, true)
			applyAgreementShadowTelemetry(ctx, &p, res)
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			svc.recordAgreementShadow(ctx, log, p)
			segment := store.state.SegmentID
			p.RequestID = "completed-quality-turn"
			p.Timestamp = p.Timestamp.Add(time.Second)
			p.ErrorClass = quality
			applyAgreementShadowTelemetry(ctx, &p, res)
			svc.recordAgreementShadow(ctx, log, p)
			if store.state.SegmentID != segment || store.state.TwoUp.Current.Target.Model != agreementSol || store.state.TwoUp.Revision != 2 {
				t.Fatalf("completed quality label discarded valid classifier evidence: %+v", store.state)
			}
		})
	}
}

func TestAgreementShadowStillGapsCommittedStreamFailures(t *testing.T) {
	for _, transport := range []TurnErrorClass{TurnErrorStreamCut, TurnErrorStreamStalled, TurnErrorTimeout, TurnErrorClientCanceled, TurnErrorOther} {
		t.Run(string(transport), func(t *testing.T) {
			store := &agreementTelemetry{auxTelemetryRepo: &auxTelemetryRepo{}}
			svc := &Service{telemetry: store}
			p, res := agreementFixture()
			p.ErrorClass = transport
			applyAgreementShadowTelemetry(agreementContext(policyregistry.TargetInternal, true), &p, res)
			svc.recordAgreementShadow(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), p)
			if store.state.SegmentID != "" || store.state.TwoUp.Current.Target != (switchpolicy.Target{}) {
				t.Fatalf("HTTP 200 masked a terminal stream failure: %+v", store.state)
			}
		})
	}
}

type agreementCommandTelemetry struct {
	*auxTelemetryRepo
	memoryShadow
}

func TestAgreementShadowForceUnforceBeforeToolResultBreaksVotes(t *testing.T) {
	ctx := router.WithStrategy(agreementContext(policyregistry.TargetInternal, true), router.StrategyHMM)
	env, err := translate.ParseAnthropic([]byte(`{"model":"weave-auto","messages":[{"role":"user","content":"synthetic task"},{"role":"assistant","content":[{"type":"tool_use","id":"synthetic-call","name":"synthetic-tool","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"synthetic-call","content":"synthetic result"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	threadKey := deriveSessionKeyForRequest(ctx, env, "synthetic-api-key")
	controlKey := deriveForceModelSessionKeyForRequest(ctx, env, "synthetic-api-key", threadKey)
	installationID := uuid.New()
	p, initial := agreementFixture()
	p.InstallationID, p.SessionKey = installationID.String(), threadKey[:]
	p.Timestamp = time.Now().Add(-time.Second)
	store := &agreementCommandTelemetry{auxTelemetryRepo: &auxTelemetryRepo{}}
	pins := newForceModelMapStore()
	if err := pins.Upsert(ctx, sessionpin.Pin{InstallationID: installationID, SessionKey: threadKey, Role: sessionpin.DefaultRole,
		Model: catalog.ModelGPT6Luna, Provider: providers.ProviderOpenAI, PolicyGroup: string(escalation.Low), Strategy: router.StrategyHMM,
		Reason: "hmm_policy", PinnedUntil: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, nil, nil, false, nil, pins, false, providers.ProviderOpenAI, catalog.ModelGPT6Luna, store).
		WithDeploymentKeyedProviders(keyed(providers.ProviderOpenAI)).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: &authorityShadowTestRouter{decision: initial.Fresh}, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
	applyAgreementShadowTelemetry(ctx, &p, initial)
	key := p.agreementShadow.Key
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc.recordAgreementShadow(ctx, log, p)
	before, _, _ := store.GetAgreementShadow(ctx, key)
	if before.TwoUp.Votes != 1 {
		t.Fatalf("fixture did not establish the pre-command vote: %+v", before)
	}
	for _, command := range []translate.ForceModelResult{{Model: agreementSol}, {Clear: true}} {
		if err := svc.handleForceModelCommand(ctx, httptest.NewRecorder(), typedCommandEnv(t, "/fm sol"), command, installationID, threadKey, controlKey, 1); err != nil {
			t.Fatal(err)
		}
		svc.grantPostCommandContinuation(ctx, installationID, threadKey, sessionpin.DefaultRole)
		boundary, _, err := store.GetAgreementShadow(ctx, key)
		if err != nil || boundary.SegmentID != "" || boundary.OneUp.Current.Target != (switchpolicy.Target{}) || boundary.TwoUp.Current.Target != (switchpolicy.Target{}) {
			t.Fatalf("successful command left simulated histories connected: %+v %v", boundary, err)
		}
	}
	routed, err := svc.runTurnLoop(ctx, env, env.RoutingFeatures(false), "synthetic-api-key", installationID, "", http.Header{}, router.Request{RequestedModel: string(router.ModelAutomatic)})
	if err != nil {
		t.Fatal(err)
	}
	p.RequestID, p.Timestamp = "next-tool-result", time.Now()
	p.DecisionModel, p.DecisionProvider = routed.Decision.Model, routed.Decision.Provider
	applyAgreementShadowTelemetry(ctx, &p, routed)
	if p.agreementShadow == nil || p.agreementShadow.Observation.Gap != switchpolicy.ShadowGapNone {
		t.Fatalf("tool result did not reach ordinary classification: %+v", p.agreementShadow)
	}
	svc.recordAgreementShadow(ctx, log, p)
	after, _, _ := store.GetAgreementShadow(ctx, key)
	if after.SegmentID == before.SegmentID || after.TwoUp.Current.Target.Model != catalog.ModelGPT6Luna || after.TwoUp.Votes != 1 || after.TwoUp.Revision != 1 {
		t.Fatalf("tool result reused a vote from before force/unforce: %+v", after)
	}
}

func TestAgreementShadowRejectedAndFailedCommandsKeepHistory(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "persistence-failed"}[failed], func(t *testing.T) {
			ctx := agreementContext(policyregistry.TargetInternal, true)
			store := &agreementCommandTelemetry{auxTelemetryRepo: &auxTelemetryRepo{}}
			pins := sessionpin.Store(newForceModelMapStore())
			if failed {
				pins = &failedCommandStore{newForceModelMapStore()}
			}
			svc := NewService(nil, nil, nil, false, nil, pins, false, providers.ProviderOpenAI, catalog.ModelGPT6Luna, store).WithDeploymentKeyedProviders(keyed(providers.ProviderOpenAI))
			p, res := agreementFixture()
			threadKey := [sessionpin.SessionKeyLen]byte{1}
			p.SessionKey = threadKey[:]
			applyAgreementShadowTelemetry(ctx, &p, res)
			svc.recordAgreementShadow(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), p)
			before, _, _ := store.GetAgreementShadow(ctx, p.agreementShadow.Key)
			model := "synthetic-unrecognized-model"
			if failed {
				model = agreementSol
			}
			err := svc.handleForceModelCommand(ctx, httptest.NewRecorder(), typedCommandEnv(t, "/fm sol"), translate.ForceModelResult{Model: model}, uuid.MustParse(p.InstallationID), threadKey, [sessionpin.SessionKeyLen]byte{2}, 1)
			if (err != nil) != failed {
				t.Fatalf("unexpected command result: %v", err)
			}
			after, _, _ := store.GetAgreementShadow(ctx, p.agreementShadow.Key)
			if after != before {
				t.Fatalf("command without a persisted transition reset the comparison: before=%+v after=%+v", before, after)
			}
		})
	}
}
