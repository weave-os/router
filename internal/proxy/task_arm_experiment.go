package proxy

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/taskarm"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type taskArmTurn struct {
	assignment     taskarm.Assignment
	assigned       bool
	cluster        string
	controlModel   string
	controlCluster string
	ceilingBinds   bool
	capped         bool
	cappedModel    string
}

func assignTaskArm(ctx context.Context, strategy router.Strategy, env *translate.RequestEnvelope, installationID uuid.UUID, forceModel, forceCluster string) taskArmTurn {
	treatmentPct := flags.IntOr(ctx, flags.KeyTaskArmExperimentTreatmentPct, 0)
	cluster := flags.StringOr(ctx, flags.KeyTaskArmExperimentTreatmentCluster, "")
	steps := taskarm.Steps(flags.StringOr(ctx, flags.KeyTaskArmExperimentSteps, ""))
	if treatmentPct <= 0 || escalation.Rank(escalation.Group(cluster)) < 0 || len(steps) == 0 {
		return taskArmTurn{}
	}
	routerDecides := forceModel == "" && forceCluster == "" &&
		hmm.Owns(strategy) &&
		!callerModelPassthroughActive(ctx) && !planHasDedicatedServingPolicy(ctx) && !policyPinHonoured(ctx)
	if !routerDecides || installationID == uuid.Nil {
		return taskArmTurn{}
	}
	_, messages := routingInputForRequest(ctx, env, "")
	label, found := taskarm.PreviousTurnLabel(messages)
	if !found {
		return taskArmTurn{}
	}
	subject := taskarm.Subject{InstallationID: installationID.String(), UserID: auth.UserIDFrom(ctx)}
	assignment, assigned := taskarm.Assign(subject, label, steps, treatmentPct)
	return taskArmTurn{assignment: assignment, assigned: assigned, cluster: cluster}
}

type turnRoute func(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures, apiKeyID string, installationID uuid.UUID, subAgentHint string, reqHeaders http.Header, req router.Request) (turnLoopResult, error)

type taskArmRoute func(env *translate.RequestEnvelope, feats translate.RoutingFeatures, apiKeyID string, installationID uuid.UUID, subAgentHint string, reqHeaders http.Header, req router.Request) (turnLoopResult, error)

func (turn *taskArmTurn) route(ctx context.Context, route turnRoute) taskArmRoute {
	return func(env *translate.RequestEnvelope, feats translate.RoutingFeatures, apiKeyID string, installationID uuid.UUID, subAgentHint string, reqHeaders http.Header, req router.Request) (turnLoopResult, error) {
		control, err := route(ctx, env, feats, apiKeyID, installationID, subAgentHint, reqHeaders, req)
		if err != nil || !turn.observeControl(control) {
			return control, err
		}
		req.ForceCluster = turn.cluster
		req.SkipSessionPin = true
		for _, model := range control.SessionDemotedModels {
			req.Candidates.ExcludedModels = candidates.ExcludingModel(req.Candidates.ExcludedModels, model)
		}
		cappedRes, err := route(ctx, env, feats, apiKeyID, installationID, subAgentHint, reqHeaders, req)
		if err != nil {
			cappedRes.releaseCooldownProbes()
			observability.FromContext(ctx).Warn("Task-arm ceiling cluster could not serve the turn; serving the control decision",
				"cluster", turn.cluster, "task_key", turn.assignment.TaskKey, "err", err)
			return control, nil
		}
		turn.capped, turn.cappedModel = true, cappedRes.Decision.Model
		control.Decision = cappedRes.Decision
		control.StickyHit = false
		control.PinTier = ""
		control.OverloadProbe = cappedRes.OverloadProbe
		control.CooldownProbeReleases = append(control.CooldownProbeReleases, cappedRes.CooldownProbeReleases...)
		return control, nil
	}
}

func (turn *taskArmTurn) observeControl(control turnLoopResult) bool {
	if !turn.assigned {
		return false
	}
	if isUserForcedReason(control.Decision.Reason) {
		*turn = taskArmTurn{}
		return false
	}
	turn.controlModel = control.Decision.Model
	turn.controlCluster = servedPolicyGroup(control)
	turn.ceilingBinds = escalation.Rank(escalation.Group(turn.controlCluster)) > escalation.Rank(escalation.Group(turn.cluster))
	return turn.ceilingBinds && turn.assignment.Arm == taskarm.ArmTreatment
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

func applyTaskArmAttrs(b *telemetry.AttrBuilder, turn taskArmTurn, servedModel string) {
	if !turn.assigned {
		return
	}
	b.String("routing.task_arm_experiment_arm", string(turn.assignment.Arm)).
		Float64("routing.task_arm_experiment_propensity", turn.assignment.Propensity).
		String("routing.task_arm_experiment_task_key", turn.assignment.TaskKey).
		String("routing.task_arm_experiment_step", turn.assignment.Step).
		String("routing.task_arm_experiment_control_model", turn.controlModel).
		String("routing.task_arm_experiment_control_cluster", turn.controlCluster).
		Bool("routing.task_arm_experiment_ceiling_binds", turn.ceilingBinds).
		Bool("routing.task_arm_experiment_treatment_applied", turn.capped && servedModel == turn.cappedModel)
}

func forceClusterForTurn(ctx context.Context, strategy router.Strategy, r *http.Request, env *translate.RequestEnvelope, feats translate.RoutingFeatures, installationID uuid.UUID, forceModel string) (string, taskArmTurn, error) {
	forceCluster, err := applyForceClusterHeader(ctx, strategy, r)
	if err != nil || handoffFromContext(ctx) != nil {
		return forceCluster, taskArmTurn{}, err
	}
	hint := subAgentHintForRequest(ctx, r.Header.Get("x-weave-subagent-type"))
	turnType := turntype.Detect(env, feats, hint, openCodeCaller(ClientIdentityFrom(ctx)))
	if turnType != turntype.MainLoop && turnType != turntype.ToolResult {
		return forceCluster, taskArmTurn{}, nil
	}
	return forceCluster, assignTaskArm(ctx, strategy, env, installationID, forceModel, forceCluster), nil
}

func applyExperimentAttrs(ctx context.Context, b *telemetry.AttrBuilder, routed *turnLoopResult, turn taskArmTurn, requestedModel, servedModel string) {
	applyBlindExperimentOutcomeAttrs(ctx, b, routed, requestedModel, servedModel)
	applyTaskArmAttrs(b, turn, servedModel)
}

func routesWithoutSessionPin(store sessionpin.Store, req router.Request) bool {
	return store == nil || req.SkipSessionPin
}
