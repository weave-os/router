package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
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
	session := [sessionpin.SessionKeyLen]byte{1}
	hmm := router.WithStrategy(context.Background(), router.StrategyHMMEmbedding)
	cases := []struct {
		name         string
		ctx          context.Context
		body         string
		session      [sessionpin.SessionKeyLen]byte
		forceModel   string
		forceCluster string
		assigned     bool
		wantCluster  string
	}{
		{"default flags leave every turn outside the experiment", hmm, taskArmLabelledHistory, session, "", "", false, ""},
		{"a treated task is constrained to the treatment cluster", taskArmContext(), taskArmLabelledHistory, session, "", "", true, "medium"},
		{"a forced model keeps the turn outside the experiment", taskArmContext(), taskArmLabelledHistory, session, "claude-opus-5-5", "", false, ""},
		{"a forced cluster keeps the turn outside the experiment", taskArmContext(), taskArmLabelledHistory, session, "", "high", false, "high"},
		{"a strategy without clusters keeps the turn outside the experiment",
			flags.WithOverrides(router.WithStrategy(context.Background(), router.StrategyRL), mustTaskArmOverrides(taskArmContext())),
			taskArmLabelledHistory, session, "", "", false, ""},
		{"a turn without a previous label is outside the experiment", taskArmContext(),
			`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"write the exporter"}]}`, session, "", "", false, ""},
		{"a request without a session is outside the experiment", taskArmContext(), taskArmLabelledHistory, [sessionpin.SessionKeyLen]byte{}, "", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn := assignTaskArm(tc.ctx, taskArmEnvelope(t, tc.body), tc.session, tc.forceModel, tc.forceCluster)

			assert.Equal(t, tc.assigned, turn.assigned)
			assert.Equal(t, tc.wantCluster, turn.forcedCluster(tc.forceCluster))
			attrs := telemetry.NewAttrBuilder(5)
			applyTaskArmAttrs(attrs, turn)
			if tc.assigned {
				assert.Len(t, attrs.Build(), 5)
			} else {
				assert.Empty(t, attrs.Build())
			}
		})
	}
}

func mustTaskArmOverrides(ctx context.Context) flags.Overrides {
	overrides, _ := flags.OverridesFromContext(ctx)
	return overrides
}

func TestTaskArmTurnRoute(t *testing.T) {
	unservable := &policy.ForcedClusterUnservableError{Cluster: "medium", Reason: "no model"}
	treated := taskArmTurn{assignment: taskarm.Assignment{Arm: taskarm.ArmTreatment}, assigned: true, cluster: "medium"}
	control := taskArmTurn{assignment: taskarm.Assignment{Arm: taskarm.ArmControl}, assigned: true, cluster: "medium"}
	other := errors.New("sidecar down")
	cases := []struct {
		name         string
		turn         taskArmTurn
		firstErr     error
		servedGroup  string
		wantClusters []string
		wantErr      error
		wantServed   bool
	}{
		{"a treatment turn served from the treatment cluster is routed once", treated, nil, "medium", []string{"medium"}, nil, true},
		{"a treatment turn a session pin kept on another cluster is not counted as served", treated, nil, "high", []string{"medium"}, nil, false},
		{"a treatment turn served by a session pin already in the treatment cluster counts as served", treated, nil, "pin:medium", []string{"medium"}, nil, true},
		{"a treatment turn served by a session pin in another cluster is not counted as served", treated, nil, "pin:high", []string{"medium"}, nil, false},
		{"an unservable treatment cluster is routed again as control", treated, unservable, "medium", []string{"medium", ""}, nil, false},
		{"another routing error is returned unchanged", treated, other, "", []string{"medium"}, other, false},
		{"a control turn never retries", control, unservable, "", []string{""}, unservable, false},
		{"a control turn the policy put in the treatment cluster is not counted as served", control, nil, "medium", []string{""}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn := tc.turn
			var clusters []string
			req := router.Request{ForceCluster: turn.forcedCluster("")}

			route := turnRoute(func(_ context.Context, _ *translate.RequestEnvelope, _ translate.RoutingFeatures, _ string, _ uuid.UUID, _ string, _ http.Header, req router.Request) (turnLoopResult, error) {
				clusters = append(clusters, req.ForceCluster)
				if len(clusters) == 1 && tc.firstErr != nil {
					return turnLoopResult{}, tc.firstErr
				}
				if group, pinned := strings.CutPrefix(tc.servedGroup, "pin:"); pinned {
					return turnLoopResult{StickyHit: true, PinPolicyGroup: group}, nil
				}
				return turnLoopResult{Decision: router.Decision{Metadata: &router.RoutingMetadata{PolicyGroup: tc.servedGroup}}}, nil
			})
			_, err := turn.route(context.Background(), route)(nil, translate.RoutingFeatures{}, "", uuid.Nil, "", nil, req)

			assert.Equal(t, tc.wantClusters, clusters)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.wantServed, turn.treatmentServed())
		})
	}
}

func TestForceClusterForTurn(t *testing.T) {
	session := [sessionpin.SessionKeyLen]byte{1}
	cases := []struct {
		name        string
		headers     map[string]string
		assigned    bool
		wantCluster string
	}{
		{"a main-loop turn of an eligible task is assigned", nil, true, "medium"},
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

			cluster, turn, err := forceClusterForTurn(ctx, r, env, env.RoutingFeatures(), session, "")

			require.NoError(t, err)
			assert.Equal(t, tc.assigned, turn.assigned)
			assert.Equal(t, tc.wantCluster, cluster)
		})
	}
}
