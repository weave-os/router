package proxy

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type conditionalForceScoringKey struct{}

func conditionalForceScoring(ctx context.Context) bool {
	scoring, _ := ctx.Value(conditionalForceScoringKey{}).(bool)
	return scoring
}

func parseConditionalForceModel(spec string) (string, escalation.Group, error) {
	fields := strings.Fields(spec)
	if len(fields) < 2 {
		return spec, "", nil
	}
	group := escalation.Group(strings.ToLower(fields[len(fields)-1]))
	if escalation.Rank(group) < 0 {
		if strings.Contains(spec, "--group") {
			return "", "", fmt.Errorf("unknown score group %q", fields[len(fields)-1])
		}
		return spec, "", nil
	}
	modelFields := fields[:len(fields)-1]
	if modelFields[len(modelFields)-1] == "--group" {
		modelFields = modelFields[:len(modelFields)-1]
	}
	if len(modelFields) == 0 {
		return "", "", fmt.Errorf("a conditional force requires a model")
	}
	return strings.Join(modelFields, " "), group, nil
}

func conditionalForceStrategyError(ctx context.Context) error {
	strategy := router.StrategyFromContext(ctx)
	_, policyPinned := router.HonouredPolicyPin(ctx)
	if (!router.IsHMMStrategy(strategy) && strategy != router.StrategyLLMClassifier) || auth.RoutingPassthroughFrom(ctx) || policyPinned {
		return fmt.Errorf("conditional forcing requires an unpinned classifier strategy; current strategy is %q", strategy)
	}
	return nil
}

func (s *Service) runConditionalForceTurn(
	ctx context.Context,
	req router.Request,
	res turnLoopResult,
	env *translate.RequestEnvelope,
	features translate.RoutingFeatures,
	apiKeyID, subAgentHint string,
	headers http.Header,
	threadKey [sessionpin.SessionKeyLen]byte,
	forced sessionpin.Pin,
) (turnLoopResult, error) {
	if err := conditionalForceStrategyError(ctx); err != nil {
		return res, err
	}
	var err error
	scoringContext := context.WithValue(ctx, conditionalForceScoringKey{}, true)
	res, err = s.runTurnLoop(scoringContext, env, features, apiKeyID, res.InstallationID, subAgentHint, headers, req)
	if err != nil {
		return res, err
	}
	group := escalation.Group(decisionPolicyGroup(res.Fresh))
	if escalation.Rank(group) < 0 || req.Unscorable || res.Fresh.Reason == policy.UnscorableHMMDecisionReason {
		return res, fmt.Errorf("conditional force cannot classify this turn: score group %q", group)
	}
	if string(group) != forced.PolicyGroup {
		return res, nil
	}
	binding, reason := s.forcedModelBinding(ctx, forced.Model, forced.Provider)
	if reason != "" {
		return res, &ForcedModelExcludedError{Model: forced.Model, Reason: reason}
	}
	forced.Provider = binding
	req.ExcludedModels = s.readmitForcedModel(ctx, req, env, features, forced)
	if !forcedPinEligible(forced, req) {
		return res, &ForcedModelExcludedError{Model: forced.Model, Reason: forcedPinIneligibilityReason(forced, req)}
	}
	res.PinModel = forced.Model
	res.PinAgeSec = pinAge(forced)
	res.SessionDisabledProviders = slices.DeleteFunc(slices.Clone(res.SessionDisabledProviders), func(provider string) bool { return provider == forced.Provider })
	res, err = s.routeWithoutPin(ctx, req, res, nil, true, forced)
	if err != nil {
		return res, err
	}
	metadata := *res.Fresh.Metadata
	metadata.PolicyRouteKey = ""
	metadata.ChosenScore = 0
	metadata.CandidateModels = []string{forced.Model}
	metadata.CandidateProviders = map[string]string{forced.Model: forced.Provider}
	metadata.CandidateScores = nil
	metadata.CandidateArmIDs = nil
	metadata.CandidateArmScores = nil
	metadata.CandidateArmProviders = nil
	metadata.SelectedArmID = ""
	metadata.SelectedRosterArmID = ""
	metadata.SelectedUpstreamID = ""
	metadata.SelectionTrace = nil
	metadata.RescueModels = nil
	metadata.SidecarRescuePool = nil
	metadata.RosterFailover = false
	metadata.AuthoritativePerTurnSelection = true
	res.Decision.Metadata = &metadata
	s.anchorForceModelHistory(ctx, res.InstallationID, threadKey, res.PinRole, forced)
	observability.FromContext(ctx).Info("Conditional force-model applied", "group", group, "model", forced.Model, "provider", forced.Provider)
	return res, nil
}
