package proxy

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/taskarm"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type taskArmTurn struct {
	assignment    taskarm.Assignment
	assigned      bool
	cluster       string
	unservable    bool
	servedCluster string
}

func assignTaskArm(ctx context.Context, env *translate.RequestEnvelope, sessionKey [sessionpin.SessionKeyLen]byte, forceModel, forceCluster string) taskArmTurn {
	treatmentPct := flags.IntOr(ctx, flags.KeyTaskArmExperimentTreatmentPct, 0)
	cluster := flags.StringOr(ctx, flags.KeyTaskArmExperimentTreatmentCluster, "")
	steps := taskarm.Steps(flags.StringOr(ctx, flags.KeyTaskArmExperimentSteps, ""))
	if treatmentPct <= 0 || cluster == "" || len(steps) == 0 {
		return taskArmTurn{}
	}
	routerDecides := forceModel == "" && forceCluster == "" &&
		router.IsHMMStrategy(router.StrategyFromContext(ctx)) &&
		!callerModelPassthroughActive(ctx) && !planHasDedicatedServingPolicy(ctx) && !policyPinHonoured(ctx)
	if !routerDecides || sessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return taskArmTurn{}
	}
	_, messages := routingInputForRequest(ctx, env, "")
	label, found := taskarm.PreviousTurnLabel(messages)
	if !found {
		return taskArmTurn{}
	}
	assignment, assigned := taskarm.Assign(sessionKey[:], label, steps, treatmentPct)
	return taskArmTurn{assignment: assignment, assigned: assigned, cluster: cluster}
}

func (turn taskArmTurn) treated() bool {
	return turn.assigned && turn.assignment.Arm == taskarm.ArmTreatment && !turn.unservable
}

func (turn taskArmTurn) treatmentServed() bool {
	return turn.treated() && turn.servedCluster == turn.cluster
}

func (turn taskArmTurn) forcedCluster(current string) string {
	if turn.treated() {
		return turn.cluster
	}
	return current
}

type turnRoute func(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures, apiKeyID string, installationID uuid.UUID, subAgentHint string, reqHeaders http.Header, req router.Request) (turnLoopResult, error)

type taskArmRoute func(env *translate.RequestEnvelope, feats translate.RoutingFeatures, apiKeyID string, installationID uuid.UUID, subAgentHint string, reqHeaders http.Header, req router.Request) (turnLoopResult, error)

func (turn *taskArmTurn) route(ctx context.Context, route turnRoute) taskArmRoute {
	return func(env *translate.RequestEnvelope, feats translate.RoutingFeatures, apiKeyID string, installationID uuid.UUID, subAgentHint string, reqHeaders http.Header, req router.Request) (turnLoopResult, error) {
		res, err := route(ctx, env, feats, apiKeyID, installationID, subAgentHint, reqHeaders, req)
		if err == nil || !turn.treated() || !errors.Is(err, policy.ErrForcedClusterUnservable) {
			turn.settle(res)
			return res, err
		}
		res.releaseCooldownProbes()
		turn.unservable = true
		req.ForceCluster = ""
		observability.FromContext(ctx).Warn("Task-arm treatment cluster could not serve the turn; routing as control",
			"cluster", turn.cluster, "task_key", turn.assignment.TaskKey)
		res, err = route(ctx, env, feats, apiKeyID, installationID, subAgentHint, reqHeaders, req)
		turn.settle(res)
		return res, err
	}
}

func (turn *taskArmTurn) settle(res turnLoopResult) {
	if isUserForcedReason(res.Decision.Reason) {
		*turn = taskArmTurn{}
		return
	}
	turn.servedCluster = servedPolicyGroup(res)
}

func policyPinHonoured(ctx context.Context) bool {
	_, honoured := router.HonouredPolicyPin(ctx)
	return honoured
}

func servedPolicyGroup(res turnLoopResult) string {
	if group := decisionPolicyGroup(res.Decision); group != "" || !res.StickyHit {
		return group
	}
	return res.PinPolicyGroup
}

func applyTaskArmAttrs(b *telemetry.AttrBuilder, turn taskArmTurn) {
	if !turn.assigned {
		return
	}
	b.String("routing.task_arm_experiment_arm", string(turn.assignment.Arm)).
		Float64("routing.task_arm_experiment_propensity", turn.assignment.Propensity).
		String("routing.task_arm_experiment_task_key", turn.assignment.TaskKey).
		String("routing.task_arm_experiment_step", turn.assignment.Step).
		Bool("routing.task_arm_experiment_treatment_applied", turn.treatmentServed())
}

func forceClusterForTurn(ctx context.Context, r *http.Request, env *translate.RequestEnvelope, feats translate.RoutingFeatures, sessionKey [sessionpin.SessionKeyLen]byte, forceModel string) (string, taskArmTurn, error) {
	forceCluster, err := applyForceClusterHeader(ctx, r)
	if err != nil || handoffFromContext(ctx) != nil {
		return forceCluster, taskArmTurn{}, err
	}
	hint := subAgentHintForRequest(ctx, r.Header.Get("x-weave-subagent-type"))
	turnType := turntype.Detect(env, feats, hint, openCodeCaller(ClientIdentityFrom(ctx)))
	if turnType != turntype.MainLoop && turnType != turntype.ToolResult {
		return forceCluster, taskArmTurn{}, nil
	}
	turn := assignTaskArm(ctx, env, sessionKey, forceModel, forceCluster)
	return turn.forcedCluster(forceCluster), turn, nil
}

func applyExperimentAttrs(ctx context.Context, b *telemetry.AttrBuilder, routed *turnLoopResult, turn taskArmTurn, requestedModel, servedModel string) {
	applyBlindExperimentOutcomeAttrs(ctx, b, routed, requestedModel, servedModel)
	applyTaskArmAttrs(b, turn)
}
