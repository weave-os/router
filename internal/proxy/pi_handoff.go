package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/armid"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

type preparedHandoff struct {
	Bypass       bool   `json:"bypass,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
	Token        string `json:"token,omitempty"`
	SummaryToken string `json:"summary_token,omitempty"`
	Model        string `json:"model,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Complexity   string `json:"complexity,omitempty"`
}

func writePreparedHandoff(w http.ResponseWriter, prepared preparedHandoff) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	return json.NewEncoder(w).Encode(prepared)
}

func (s *Service) finishHandoffPreparation(ctx context.Context, strategy router.Strategy, w http.ResponseWriter, env *translate.RequestEnvelope, req router.Request, route routingpolicy.TurnRoute) error {
	if route.UsageBypass || route.HardPinned || isUserForcedReason(route.Decision.Reason) {
		return writePreparedHandoff(w, preparedHandoff{Bypass: true})
	}
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	claims := conversation.HandoffClaims{
		Purpose: conversation.HandoffContinuation, APIKeyID: apiKeyID, SessionID: req.ClientSessionID,
		RequestedModel: req.RequestedModel, Tools: req.ToolConfigurationSHA256,
		Reasoning: req.ReasoningConfigurationSHA256, Route: route,
	}
	token, err := s.lifecycle.PiHandoff(s.clock.Now).Mint(claims)
	if err != nil {
		return fmt.Errorf("sign Pi continuation: %w", err)
	}
	prepared := preparedHandoff{
		Token: token, Model: route.Decision.Model, Provider: route.Decision.Provider,
		Complexity: decisionPolicyGroup(route.Decision),
	}
	prepared.SessionToken, err = mintPiSession(ctx, s.lifecycle.PiHandoff(s.clock.Now), env, deriveSessionKeyForRequest(ctx, env, apiKeyID))
	if err != nil {
		return fmt.Errorf("sign Pi session: %w", err)
	}
	if prepared.Complexity == "" && route.Decision.Model == route.PinModel {
		prepared.Complexity = route.PinPolicyGroup
	}
	previousModel, previousEffort := armid.SplitEffort(route.PriorServedModel)
	previousProvider := ""
	if route.PinModel == previousModel {
		previousProvider = route.PinProvider
	}
	if previousModel != "" && s.pinStore != nil && (s.strategyPorts.Ownership.Owns(route.Strategy) || isHMMTurn(s.strategyPorts, route)) {
		history := loadSwitchHistory(ctx, s.strategyPorts, routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy), route.SessionKey, route.PinRole)
		if history.LastServedModel == route.PriorServedModel {
			previousProvider = history.Provider
		}
	}
	if previousModel != "" && previousProvider != "" && previousModel != route.Decision.Model {
		claims = conversation.SummaryHandoff(claims, sessionpin.Pin{Model: previousModel, Provider: previousProvider, Effort: previousEffort, Reason: "pi_handoff_summary"})
		prepared.SummaryToken, err = s.lifecycle.PiHandoff(s.clock.Now).Mint(claims)
		if err != nil {
			return fmt.Errorf("sign Pi summary: %w", err)
		}
	}
	return writePreparedHandoff(w, prepared)
}

func (s *Service) resumeHandoff(ctx context.Context, strategy router.Strategy, env *translate.RequestEnvelope, req router.Request, isSubagent bool) (routingpolicy.TurnRoute, error) {
	claims := conversation.HandoffFromContext(ctx)
	if claims.Route.Strategy != strategy ||
		claims.SessionID != clientSessionIDForRequest(ctx, env) ||
		claims.RequestedModel != req.RequestedModel {
		return routingpolicy.TurnRoute{}, conversation.ErrHandoffInvalid
	}
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	forceKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKeyID, claims.Route.SessionKey)
	_, forced, _ := routingpolicy.NewForceModelPins(s.pinStore, s.clock.Now).Load(ctx, forceKey, sessionKeyHex(claims.Route.SessionKey), isSubagent)
	if req.ForceModel != "" || forced {
		return routingpolicy.TurnRoute{}, conversation.ErrHandoffInvalid
	}
	if claims.Purpose == conversation.HandoffContinuation && (claims.Tools != req.ToolConfigurationSHA256 || claims.Reasoning != req.ReasoningConfigurationSHA256) {
		return routingpolicy.TurnRoute{}, conversation.ErrHandoffInvalid
	}
	var err error
	req, err = s.applyTranslationPlan(ctx, req)
	if err != nil {
		return routingpolicy.TurnRoute{}, err
	}
	req.Candidates.AutomaticExcludedModels = s.globalAutomaticExcludedModels(ctx)
	selected := claims.Route.Decision
	pin := sessionpin.Pin{Model: selected.Model, Provider: selected.Provider}
	if !candidates.AutomaticPinEligible(pin, req) {
		return routingpolicy.TurnRoute{}, conversation.ErrHandoffInvalid
	}
	return claims.Route, nil
}

func validateHandoffEnvelope(ctx context.Context, env *translate.RequestEnvelope, headers http.Header, body []byte) error {
	claims := conversation.HandoffFromContext(ctx)
	if claims == nil {
		return nil
	}
	if session := conversation.PiSessionFromContext(ctx); session != nil && claims.Route.SessionKey != ([sessionpin.SessionKeyLen]byte{}) && session.SessionKey != claims.Route.SessionKey {
		return conversation.ErrHandoffInvalid
	}
	_, beta := env.ExtractBetaCommand()
	_, force := env.ExtractForceModelCommand()
	_, feedback := env.ExtractRouterFeedbackCommand()
	_, serverTool := websearch.FindServerTool(body)
	if beta || force || feedback || serverTool || headers.Get(requestcontext.ForceModelHeader) != "" ||
		claims.SessionID != clientSessionIDForRequest(ctx, env) || claims.RequestedModel != env.Model() {
		return conversation.ErrHandoffInvalid
	}
	return nil
}

func (s *Service) canPrepareHandoff(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures, headers http.Header, body []byte) bool {
	_, shadow := requestcontext.AgentShadowEvalFromContext(ctx)
	_, betaCommand := env.ExtractBetaCommand()
	_, forceCommand := env.ExtractForceModelCommand()
	_, feedbackCommand := env.ExtractRouterFeedbackCommand()
	_, serverTool := websearch.FindServerTool(body)
	return !shadow && !billing.SubscriptionOnlyFromContext(ctx) &&
		strings.HasPrefix(env.MetadataUserID(), "pi:") && headers.Get(requestcontext.ForceModelHeader) == "" &&
		!betaCommand && !forceCommand && !feedbackCommand && !serverTool &&
		authoritativePolicyTurn(turntype.DetectFromEnvelope(env, feats, ""))
}
