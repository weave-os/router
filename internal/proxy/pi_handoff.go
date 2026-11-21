package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/armid"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"
)

const piHandoffTTL = 10 * time.Minute
const piHandoffField = "weave_handoff"

type handoffPurpose string

const (
	handoffContinuation handoffPurpose = "continuation"
	handoffSummary      handoffPurpose = "summary"
)

type handoffPreparationKey struct{}
type handoffClaimsKey struct{}

var ErrHandoffUnavailable = errors.New("pi handoff requires a shared ROUTER_PI_HANDOFF_SECRET of at least 32 bytes")

var ErrHandoffInvalid = errors.New("pi handoff expired or no longer matches this request; prepare the route again")

const (
	handoffUnavailableMessage = "Pi handoff requires a shared ROUTER_PI_HANDOFF_SECRET of at least 32 bytes"
	handoffInvalidMessage     = "Pi handoff expired or no longer matches this request; prepare the route again"
)

type handoffClaims struct {
	jwt.RegisteredClaims
	Purpose        handoffPurpose `json:"purpose"`
	APIKeyID       string         `json:"key"`
	SessionID      string         `json:"session"`
	RequestedModel string         `json:"requested_model"`
	Tools          string         `json:"tools"`
	Reasoning      string         `json:"reasoning"`
	Route          turnLoopResult `json:"route"`
}

type preparedHandoff struct {
	Bypass       bool   `json:"bypass,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
	Token        string `json:"token,omitempty"`
	SummaryToken string `json:"summary_token,omitempty"`
	Model        string `json:"model,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Complexity   string `json:"complexity,omitempty"`
}

func (s *Service) WithPiHandoffSecret(secret string) *Service {
	if len(secret) >= 32 {
		s.piHandoffSecret = []byte(secret)
	}
	return s
}

func WithHandoffPreparation(ctx context.Context) context.Context {
	return context.WithValue(ctx, handoffPreparationKey{}, true)
}

func preparingHandoff(ctx context.Context) bool {
	preparing, _ := ctx.Value(handoffPreparationKey{}).(bool)
	return preparing
}

func handoffFromContext(ctx context.Context) *handoffClaims {
	claims, _ := ctx.Value(handoffClaimsKey{}).(*handoffClaims)
	return claims
}

func writePreparedHandoff(w http.ResponseWriter, prepared preparedHandoff) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	return json.NewEncoder(w).Encode(prepared)
}

func (s *Service) parseHandoff(ctx context.Context, body []byte) (context.Context, []byte, error) {
	ctx, body, err := s.parsePiSession(ctx, body)
	if err != nil {
		return ctx, body, err
	}
	encoded := gjson.GetBytes(body, piHandoffField)
	if preparingHandoff(ctx) && len(s.piHandoffSecret) == 0 {
		return ctx, body, ErrHandoffUnavailable
	}
	if !encoded.Exists() {
		return ctx, body, nil
	}
	if len(s.piHandoffSecret) == 0 || preparingHandoff(ctx) || encoded.Type != gjson.String {
		return ctx, body, ErrHandoffInvalid
	}
	claims := &handoffClaims{}
	_, err = jwt.ParseWithClaims(encoded.String(), claims, func(_ *jwt.Token) (any, error) {
		return s.piHandoffSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(), jwt.WithIssuer("weave-pi-handoff-v1"), jwt.WithTimeFunc(s.clock.Now))
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	if err != nil || apiKeyID == "" || claims.APIKeyID != apiKeyID ||
		claims.Route.InstallationID != installationIDFromContext(ctx) ||
		(claims.Purpose != handoffContinuation && claims.Purpose != handoffSummary) {
		return ctx, body, ErrHandoffInvalid
	}
	clean, err := sjson.DeleteBytes(body, piHandoffField)
	return context.WithValue(ctx, handoffClaimsKey{}, claims), clean, err
}

func (s *Service) mintHandoff(claims handoffClaims) (string, error) {
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Issuer: "weave-pi-handoff-v1", ExpiresAt: jwt.NewNumericDate(s.clock.Now().Add(piHandoffTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.piHandoffSecret)
}

func (s *Service) finishHandoffPreparation(ctx context.Context, strategy router.Strategy, w http.ResponseWriter, env *translate.RequestEnvelope, req router.Request, route turnLoopResult) error {
	if route.UsageBypass || route.HardPinned || isUserForcedReason(route.Decision.Reason) {
		return writePreparedHandoff(w, preparedHandoff{Bypass: true})
	}
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	claims := handoffClaims{
		Purpose: handoffContinuation, APIKeyID: apiKeyID, SessionID: req.ClientSessionID,
		RequestedModel: req.RequestedModel, Tools: req.ToolConfigurationSHA256,
		Reasoning: req.ReasoningConfigurationSHA256, Route: route,
	}
	token, err := s.mintHandoff(claims)
	if err != nil {
		return fmt.Errorf("sign Pi continuation: %w", err)
	}
	prepared := preparedHandoff{
		Token: token, Model: route.Decision.Model, Provider: route.Decision.Provider,
		Complexity: decisionPolicyGroup(route.Decision),
	}
	prepared.SessionToken, err = s.mintPiSession(ctx, env, deriveSessionKeyForRequest(ctx, env, apiKeyID))
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
		history := routingpolicy.NewSessionPins(s.pinStore, s.clock.Now, strategy).LoadHMMHistory(ctx, route.SessionKey, route.PinRole)
		if history.LastServedModel == route.PriorServedModel {
			previousProvider = history.Provider
		}
	}
	if previousModel != "" && previousProvider != "" && previousModel != route.Decision.Model {
		claims.Purpose = handoffSummary
		claims.Route.Decision = sessionpin.Pin{Model: previousModel, Provider: previousProvider, Effort: previousEffort, Reason: "pi_handoff_summary"}.Decision()
		claims.Route.Fresh = router.Decision{}
		claims.Route.HardPinned = true
		claims.Route.AuthoritativePerTurn = false
		claims.Route.TurnType = turntype.Compaction
		claims.Route.Purpose = inference.PurposeClientCompaction
		claims.Route.Origin = policy.OverrideSourceSession
		claims.Route.EscalationOrdinal = 0
		prepared.SummaryToken, err = s.mintHandoff(claims)
		if err != nil {
			return fmt.Errorf("sign Pi summary: %w", err)
		}
	}
	return writePreparedHandoff(w, prepared)
}

func (s *Service) resumeHandoff(ctx context.Context, strategy router.Strategy, env *translate.RequestEnvelope, req router.Request, isSubagent bool) (turnLoopResult, error) {
	claims := handoffFromContext(ctx)
	if claims.Route.Strategy != strategy ||
		claims.SessionID != clientSessionIDForRequest(ctx, env) ||
		claims.RequestedModel != req.RequestedModel {
		return turnLoopResult{}, ErrHandoffInvalid
	}
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	forceKey := deriveForceModelSessionKeyForRequest(ctx, env, apiKeyID, claims.Route.SessionKey)
	_, forced, _ := routingpolicy.NewForceModelPins(s.pinStore, s.clock.Now).Load(ctx, forceKey, sessionKeyHex(claims.Route.SessionKey), isSubagent)
	if req.ForceModel != "" || forced {
		return turnLoopResult{}, ErrHandoffInvalid
	}
	if claims.Purpose == handoffContinuation && (claims.Tools != req.ToolConfigurationSHA256 || claims.Reasoning != req.ReasoningConfigurationSHA256) {
		return turnLoopResult{}, ErrHandoffInvalid
	}
	var err error
	req, err = s.applyTranslationPlan(ctx, req)
	if err != nil {
		return turnLoopResult{}, err
	}
	req.Candidates.AutomaticExcludedModels = s.globalAutomaticExcludedModels(ctx)
	selected := claims.Route.Decision
	pin := sessionpin.Pin{Model: selected.Model, Provider: selected.Provider}
	if !candidates.AutomaticPinEligible(pin, req) {
		return turnLoopResult{}, ErrHandoffInvalid
	}
	return claims.Route, nil
}

func validateHandoffEnvelope(ctx context.Context, env *translate.RequestEnvelope, headers http.Header, body []byte) error {
	claims := handoffFromContext(ctx)
	if claims == nil {
		return nil
	}
	if session := piSessionFromContext(ctx); session != nil && claims.Route.SessionKey != ([sessionpin.SessionKeyLen]byte{}) && session.SessionKey != claims.Route.SessionKey {
		return ErrHandoffInvalid
	}
	_, beta := env.ExtractBetaCommand()
	_, force := env.ExtractForceModelCommand()
	_, feedback := env.ExtractRouterFeedbackCommand()
	_, serverTool := websearch.FindServerTool(body)
	if beta || force || feedback || serverTool || headers.Get(requestcontext.ForceModelHeader) != "" ||
		claims.SessionID != clientSessionIDForRequest(ctx, env) || claims.RequestedModel != env.Model() {
		return ErrHandoffInvalid
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
