package proxy

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry/turnrecord"
)

type routeFailureRequest struct {
	RequestID      string
	RequestStart   time.Time
	RequestedModel string
	Strategy       router.Strategy
}

func (s *Service) recordPolicyPinRouteFailure(ctx context.Context, request routeFailureRequest, turnType turntype.TurnType, userPrompt bool, routeErr error) {
	if _, honoured := router.HonouredPolicyPin(ctx); !honoured {
		return
	}
	installationID := requestcontext.InstallationIDFromContext(ctx)
	if installationID == uuid.Nil {
		return
	}
	params := turnrecord.InsertTelemetryParams{
		InstallationID: installationID.String(),
		APIKeyID:       requestcontext.APIKeyIDFromContext(ctx),
		RequestID:      request.RequestID,
		SpanType:       "router.upstream",
		TraceID:        request.RequestID,
		Timestamp:      request.RequestStart,
		RequestedModel: request.RequestedModel,
		TurnType:       string(turnType),
		DecisionReason: DecisionReasonPolicyPinUnservable,
		ErrorClass:     turnrecord.TurnErrorRoutingRefused,
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
