package proxy

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/router"
	randomrouter "weave-os/router/internal/router/random_router"
	"weave-os/router/internal/router/turntype"
)

func (s *Service) recordPolicyPinRouteFailure(ctx context.Context, requestID string, requestStart time.Time, requestedModel string, turnType turntype.TurnType, userPrompt bool, routeErr error) {
	if _, honoured := router.HonouredPolicyPin(ctx); !honoured && randomBenchmarkFrom(ctx) == nil {
		return
	}
	installationID := installationIDFromContext(ctx)
	if installationID == uuid.Nil {
		return
	}
	params := InsertTelemetryParams{
		InstallationID: installationID.String(),
		APIKeyID:       apiKeyIDFromContext(ctx),
		RequestID:      requestID,
		SpanType:       "router.upstream",
		TraceID:        requestID,
		Timestamp:      requestStart,
		RequestedModel: requestedModel,
		TurnType:       string(turnType),
		DecisionReason: DecisionReasonPolicyPinUnservable,
		ErrorClass:     TurnErrorRoutingRefused,
		UserPrompt:     &userPrompt,
		Strategy:       string(router.StrategyFromContext(ctx)),
		RouterUserID:   auth.UserIDFrom(ctx),
	}
	if !errors.Is(routeErr, router.ErrPolicyPinUnavailable) {
		params.DecisionReason = DecisionReasonRoutingFailed
	}
	if profile := randomBenchmarkFrom(ctx); profile != nil {
		params.PolicyArtifactID = randomrouter.PolicyID
		params.PolicyArtifactSHA256 = profile.Config.PolicySHA256
		params.Strategy = string(router.StrategyRandomBenchmark)
		params.SessionID = ClientIdentityFrom(ctx).SessionID
		params.RolloutID = ClientIdentityFrom(ctx).RolloutID
	}
	applyBlindExperimentTelemetry(ctx, &params, nil)
	applyPolicyPinTelemetry(ctx, &params, nil)
	s.fireTelemetry(ctx, params)
}
