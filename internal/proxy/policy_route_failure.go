package proxy

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
)

type routeFailureRequest struct {
	RequestID      string
	Start          time.Time
	RequestedModel string
	Strategy       router.Strategy
}

func (s *Service) recordPolicyPinRouteFailure(ctx context.Context, request routeFailureRequest, turnType turntype.TurnType, userPrompt bool, routeErr error) {
	if _, honoured := router.HonouredPolicyPin(ctx); !honoured {
		return
	}
	installationID := installationIDFromContext(ctx)
	if installationID == uuid.Nil {
		return
	}
	params := InsertTelemetryParams{
		InstallationID: installationID.String(),
		APIKeyID:       apiKeyIDFromContext(ctx),
		RequestID:      request.RequestID,
		SpanType:       "router.upstream",
		TraceID:        request.RequestID,
		Timestamp:      request.Start,
		RequestedModel: request.RequestedModel,
		TurnType:       string(turnType),
		DecisionReason: DecisionReasonPolicyPinUnservable,
		ErrorClass:     TurnErrorRoutingRefused,
		UserPrompt:     &userPrompt,
		Strategy:       string(request.Strategy),
		RouterUserID:   auth.UserIDFrom(ctx),
	}
	if !errors.Is(routeErr, router.ErrPolicyPinUnavailable) {
		params.DecisionReason = DecisionReasonRoutingFailed
	}
	applyBlindExperimentTelemetry(ctx, &params, nil)
	applyPolicyPinTelemetry(ctx, &params, nil)
	s.fireTelemetry(ctx, params)
}
