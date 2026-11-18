package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/taskarm"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

const taskArmLabelledHistory = `{"model":"claude-opus-5-5","messages":[` +
	`{"role":"user","content":"write the exporter"},` +
	`{"role":"assistant","content":"Tests pass.\n@cycle export csv-writer verify running | Waiting on the slow suite."},` +
	`{"role":"user","content":"go on"}]}`

func taskArmContext() context.Context {
	ctx := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	return flags.WithOverrides(ctx, flags.Overrides{
		Ints:    map[flags.Key]int{flags.KeyTaskArmExperimentTreatmentPct: 100},
		Strings: map[flags.Key]string{flags.KeyTaskArmExperimentSteps: "verify,review", flags.KeyTaskArmExperimentTreatmentCluster: "medium"},
	})
}

func taskArmEnvelope(t *testing.T, body string) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	return env
}

func TestAssignTaskArm(t *testing.T) {
	installation := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	hmm := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	unrankedCluster := flags.WithOverrides(hmm, flags.Overrides{
		Ints:    map[flags.Key]int{flags.KeyTaskArmExperimentTreatmentPct: 100},
		Strings: map[flags.Key]string{flags.KeyTaskArmExperimentSteps: "verify", flags.KeyTaskArmExperimentTreatmentCluster: "bespoke"},
	})
	cases := []struct {
		name         string
		ctx          context.Context
		body         string
		installation uuid.UUID
		forceModel   string
		forceCluster string
		assigned     bool
	}{
		{"default flags leave every turn outside the experiment", hmm, taskArmLabelledHistory, installation, "", "", false},
		{"an eligible task is assigned", taskArmContext(), taskArmLabelledHistory, installation, "", "", true},
		{"a forced model keeps the turn outside the experiment", taskArmContext(), taskArmLabelledHistory, installation, "claude-opus-5-5", "", false},
		{"a forced cluster keeps the turn outside the experiment", taskArmContext(), taskArmLabelledHistory, installation, "", "high", false},
		{"a strategy without clusters keeps the turn outside the experiment",
			flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyRL), mustTaskArmOverrides(taskArmContext())),
			taskArmLabelledHistory, installation, "", "", false},
		{"a turn without a previous label is outside the experiment", taskArmContext(),
			`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"write the exporter"}]}`, installation, "", "", false},
		{"a request without an installation is outside the experiment", taskArmContext(), taskArmLabelledHistory, uuid.Nil, "", "", false},
		{"a ceiling cluster outside the cost ladder turns the experiment off", unrankedCluster, taskArmLabelledHistory, installation, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn := assignTaskArm(tc.ctx, taskArmEnvelope(t, tc.body), tc.installation, tc.forceModel, tc.forceCluster)

			assert.Equal(t, tc.assigned, turn.assigned)
			attrs := telemetry.NewAttrBuilder(8)
			applyTaskArmAttrs(attrs, turn, "")
			if tc.assigned {
				assert.Len(t, attrs.Build(), 8)
			} else {
				assert.Empty(t, attrs.Build())
			}
		})
	}
}

func TestAssignTaskArmFollowsTheTaskAcrossSessions(t *testing.T) {
	installation := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	resumedElsewhere := strings.Replace(taskArmLabelledHistory, "write the exporter", "pick the exporter back up", 1)

	first := assignTaskArm(taskArmContext(), taskArmEnvelope(t, taskArmLabelledHistory), installation, "", "")
	resumed := assignTaskArm(taskArmContext(), taskArmEnvelope(t, resumedElsewhere), installation, "", "")
	otherUser := assignTaskArm(context.WithValue(taskArmContext(), auth.UserIDContextKey{}, "user-b"), taskArmEnvelope(t, taskArmLabelledHistory), installation, "", "")

	require.True(t, first.assigned)
	assert.Equal(t, first.assignment, resumed.assignment)
	assert.NotEqual(t, first.assignment.TaskKey, otherUser.assignment.TaskKey)
}

func mustTaskArmOverrides(ctx context.Context) flags.Overrides {
	overrides, _ := flags.OverridesFromContext(ctx)
	return overrides
}

func TestTaskArmTurnRoute(t *testing.T) {
	treated := taskArmTurn{assignment: taskarm.Assignment{Arm: taskarm.ArmTreatment}, assigned: true, cluster: "medium"}
	control := taskArmTurn{assignment: taskarm.Assignment{Arm: taskarm.ArmControl}, assigned: true, cluster: "medium"}
	sidecarDown := errors.New("sidecar down")
	unservable := &policy.ForcedClusterUnservableError{Cluster: "medium", Reason: "no model"}
	cappedRequest := router.Request{ForceCluster: "medium", SkipSessionPin: true, ExcludedModels: map[string]struct{}{"struck": {}}}
	cases := []struct {
		name         string
		turn         taskArmTurn
		controlGroup string
		controlErr   error
		cappedErr    error
		wantRoutes   []router.Request
		wantServed   string
		wantBinds    bool
		wantCapped   bool
		wantErr      error
	}{
		{"a treatment turn control would route above the ceiling is routed again inside it without the session pin",
			treated, "high", nil, nil, []router.Request{{}, cappedRequest}, "capped", true, true, nil},
		{"a treatment turn a session pin holds above the ceiling is capped", treated, "pin:maximum", nil, nil,
			[]router.Request{{}, cappedRequest}, "capped", true, true, nil},
		{"a treatment turn control routes at the ceiling is served as control", treated, "medium", nil, nil, []router.Request{{}}, "control", false, false, nil},
		{"a treatment turn control routes below the ceiling is never moved up", treated, "low", nil, nil, []router.Request{{}}, "control", false, false, nil},
		{"a treatment turn outside the cost ladder is served as control", treated, "", nil, nil, []router.Request{{}}, "control", false, false, nil},
		{"a ceiling cluster that cannot serve leaves the control decision in place", treated, "high", nil, unservable,
			[]router.Request{{}, cappedRequest}, "control", true, false, nil},
		{"a control turn above the ceiling is recorded as binding and served unchanged", control, "high", nil, nil, []router.Request{{}}, "control", true, false, nil},
		{"a routing error is returned unchanged", treated, "", sidecarDown, nil, []router.Request{{}}, "", false, false, sidecarDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn := tc.turn
			var routes []router.Request
			var released []string
			route := turnRoute(func(_ context.Context, _ *translate.RequestEnvelope, _ translate.RoutingFeatures, _ string, _ uuid.UUID, _ string, _ http.Header, req router.Request) (turnLoopResult, error) {
				routes = append(routes, req)
				if req.SkipSessionPin {
					return turnLoopResult{
						Decision: router.Decision{Model: "capped"}, OverloadProbe: &breaker.Probe{Model: "capped"},
						CooldownProbeReleases: []func(){func() { released = append(released, "capped") }},
					}, tc.cappedErr
				}
				control := turnLoopResult{
					SessionKey: [sessionpin.SessionKeyLen]byte{7}, PriorServedModel: "control", EscalationOrdinal: 3,
					SessionDemotedModels: []string{"struck"}, PrefixTrimmed: true, OverloadProbe: &breaker.Probe{Model: "control"},
					CooldownProbes:        map[string]time.Time{"capped": {}},
					CooldownProbeReleases: []func(){func() { released = append(released, "control") }},
				}
				if group, pinned := strings.CutPrefix(tc.controlGroup, "pin:"); pinned {
					control.Decision = router.Decision{Model: "control"}
					control.StickyHit, control.PinPolicyGroup = true, group
					return control, tc.controlErr
				}
				control.Decision = router.Decision{Model: "control", Metadata: &router.RoutingMetadata{PolicyGroup: tc.controlGroup}}
				return control, tc.controlErr
			})

			res, err := turn.route(context.Background(), route)(nil, translate.RoutingFeatures{}, "", uuid.Nil, "", nil, router.Request{})

			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.wantRoutes, routes)
			if tc.wantErr != nil {
				return
			}
			assert.Equal(t, tc.wantServed, res.Decision.Model)
			assert.True(t, res.PrefixTrimmed, "the served turn keeps the history-trim signal of the first route")
			assert.Equal(t, [sessionpin.SessionKeyLen]byte{7}, res.SessionKey, "the served turn stays in its session for failure handling and usage")
			assert.Equal(t, "control", res.PriorServedModel, "a capped turn that changes model is seen as a model switch")
			assert.EqualValues(t, 3, res.EscalationOrdinal, "the escalation checkpoint of the first route gets the served turn's outcome")
			assert.False(t, tc.wantCapped && res.StickyHit, "a capped turn is not served by the session pin")
			assert.Contains(t, res.CooldownProbes, "capped", "a cooldown probe the session leased can still be cleared by the served turn")
			require.NotNil(t, res.OverloadProbe)
			assert.Equal(t, tc.wantServed, res.OverloadProbe.Model, "the served route's recovery probe is the one that gets the outcome")
			res.releaseCooldownProbes()
			wantReleased := []string{"control"}
			if tc.wantCapped {
				wantReleased = []string{"control", "capped"}
			} else if len(routes) == 2 {
				wantReleased = []string{"capped", "control"}
			}
			assert.Equal(t, wantReleased, released, "the session's cooldown leases stay with the served turn; an unserved capped route gives its probes back at once")
			assert.Equal(t, tc.wantBinds, turn.ceilingBinds)
			assert.Equal(t, tc.wantCapped, turn.capped)
			assert.Equal(t, strings.TrimPrefix(tc.controlGroup, "pin:"), turn.controlCluster)
		})
	}
}

func TestTaskArmTurnLeavesTheExperimentWhenASavedForcedModelServes(t *testing.T) {
	turn := taskArmTurn{assignment: taskarm.Assignment{Arm: taskarm.ArmTreatment}, assigned: true, cluster: "medium"}
	forced := router.Decision{Reason: translate.ReasonUserForceModel, Metadata: &router.RoutingMetadata{PolicyGroup: "high"}}

	capped := turn.observeControl(turnLoopResult{Decision: forced})

	assert.False(t, capped)
	assert.False(t, turn.assigned)
	attrs := telemetry.NewAttrBuilder(8)
	applyTaskArmAttrs(attrs, turn, "")
	assert.Empty(t, attrs.Build())
}

func TestForceClusterForTurn(t *testing.T) {
	installation := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	cases := []struct {
		name        string
		headers     map[string]string
		assigned    bool
		wantCluster string
	}{
		{"a main-loop turn of an eligible task is assigned and routed without a forced cluster", nil, true, ""},
		{"a sub-agent turn is left outside the experiment", map[string]string{"x-weave-subagent-type": "explore"}, false, ""},
		{"a caller's forced cluster wins and the turn is left outside the experiment", map[string]string{ForceClusterHeader: "high"}, false, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := taskArmContext()
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", nil)
			require.NoError(t, err)
			for key, value := range tc.headers {
				r.Header.Set(key, value)
			}
			env := taskArmEnvelope(t, taskArmLabelledHistory)

			cluster, turn, err := forceClusterForTurn(ctx, r, env, env.RoutingFeatures(), installation, "")

			require.NoError(t, err)
			assert.Equal(t, tc.assigned, turn.assigned)
			assert.Equal(t, tc.wantCluster, cluster)
		})
	}
}

type taskArmCeilingRouter struct{}

func (taskArmCeilingRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	if req.ForceCluster == "medium" {
		return router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-6", Reason: "ceiling", Metadata: &router.RoutingMetadata{PolicyGroup: "medium"}}, nil
	}
	return router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-opus-4-8", Reason: "control", Metadata: &router.RoutingMetadata{PolicyGroup: "high"}}, nil
}

func TestACappedTreatmentTurnLeavesTheSessionPinAsControlWouldHaveLeftIt(t *testing.T) {
	strategy := router.Strategy("task-arm-test")
	routeTurn := func(arm taskarm.Arm) (turnLoopResult, taskArmTurn, *stubPinStore) {
		store := newStubPinStore()
		store.getFound = true
		store.getPin = sessionpin.Pin{
			Provider: providers.ProviderAnthropic, Model: "claude-opus-4-7", Reason: "cluster:v0.2", PolicyGroup: "high",
			TurnCount: 3, PinnedUntil: time.Now().Add(time.Hour), LastTurnEndedAt: time.Now().Add(-time.Minute), LastOutputTokens: 9,
		}
		svc := NewService(nil, nil, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
			WithHMMDowngradeHysteresisTurns(0).
			WithPolicyStrategy(policy.StrategySpec{
				Strategy:     strategy,
				Router:       taskArmCeilingRouter{},
				Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: true},
			})
		env := taskArmEnvelope(t, taskArmLabelledHistory)
		features := env.RoutingFeatures()
		req := router.Request{RequestedModel: features.Model, EstimatedInputTokens: features.Tokens, ConversationMessages: conversationMessagesForRouting(env)}
		turn := taskArmTurn{assignment: taskarm.Assignment{Arm: arm}, assigned: true, cluster: "medium"}

		res, err := turn.route(router.WithStrategy(context.Background(), strategy), svc.runTurnLoop)(env, features, "api-key", uuid.New(), "", http.Header{}, req)

		require.NoError(t, err)
		return res, turn, store
	}

	controlRes, controlTurn, controlStore := routeTurn(taskarm.ArmControl)
	treatedRes, treatedTurn, treatedStore := routeTurn(taskarm.ArmTreatment)

	assert.Equal(t, "claude-opus-4-8", controlRes.Decision.Model)
	assert.Equal(t, "claude-sonnet-4-6", treatedRes.Decision.Model)
	assert.True(t, treatedTurn.capped)
	assert.Equal(t, controlTurn.controlModel, treatedTurn.controlModel, "both arms record the same counterfactual control decision")
	assert.True(t, controlTurn.ceilingBinds)
	assert.False(t, treatedRes.StickyHit, "the capped turn does not inherit the session pin")
	assert.Equal(t, controlRes.SessionKey, treatedRes.SessionKey)
	assert.Equal(t, controlRes.Fresh, treatedRes.Fresh, "the routing history keeps the decision control routing made")
	assert.Equal(t, append(controlStore.getRoles, forceModelSessionRole), treatedStore.getRoles, "the capped route reads the user's forced-model choice and no session pin")
	require.Len(t, treatedStore.upserts, len(controlStore.upserts))
	for i, pin := range treatedStore.upserts {
		assert.Equal(t, controlStore.upserts[i].Model, pin.Model, "the stored pin is the control decision in both arms")
	}
	assert.NotEmpty(t, controlStore.upserts)
}

func TestTreatmentAppliedFollowsTheModelThatServedTheTurn(t *testing.T) {
	turn := taskArmTurn{assignment: taskarm.Assignment{Arm: taskarm.ArmTreatment}, assigned: true, cluster: "medium", ceilingBinds: true, capped: true, cappedModel: "capped"}
	cases := []struct {
		name        string
		servedModel string
		want        bool
	}{
		{"the capped model served the turn", "capped", true},
		{"a failover served the turn with another model", "rescue", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attrs := telemetry.NewAttrBuilder(8)

			applyTaskArmAttrs(attrs, turn, tc.servedModel)

			want := telemetry.NewAttrBuilder(1).Bool("routing.task_arm_experiment_treatment_applied", tc.want).Build()[0]
			assert.Contains(t, attrs.Build(), want)
		})
	}
}
