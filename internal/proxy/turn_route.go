package proxy

import (
	"context"
	"log/slog"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
)

type routeTurnServices interface {
	recordPolicyPinRouteFailure(ctx context.Context, request routeFailureRequest, turnType turntype.TurnType, userPrompt bool, routeErr error)
}

var _ routeTurnServices = (*Service)(nil)

type routeTurnRequest struct {
	Request        router.Request
	Started        time.Time
	Failure        routeFailureRequest
	FailureMessage string
	UserPrompt     bool
}

func routeTurn(ctx context.Context, s routeTurnServices, clock auth.Clock, log *slog.Logger, turn routeTurnRequest, route func(routeCtx context.Context) (turnLoopResult, error)) (turnLoopResult, int64, error) {
	routeCtx, routeSpan := startRoutingSpan(ctx, turn.Failure.Strategy, turn.Request)
	routeRes, err := route(routeCtx)
	finishRoutingSpan(routeSpan, routeRes.Decision, err)
	routeMs := clock.Now().Sub(turn.Started).Milliseconds()
	if err != nil {
		log.Error(turn.FailureMessage, "err", err, "route_ms", routeMs, "requested_model", turn.Request.RequestedModel, "total_input_tokens", turn.Request.EstimatedInputTokens)
		s.recordPolicyPinRouteFailure(ctx, turn.Failure, routeRes.TurnType, turn.UserPrompt, err)
	}
	return routeRes, routeMs, err
}

func withRouteSessionState(ctx context.Context, routeRes turnLoopResult) context.Context {
	if len(routeRes.SessionDisabledProviders) > 0 {
		ctx = context.WithValue(ctx, SessionDisabledProvidersContextKey{}, routeRes.SessionDisabledProviders)
	}
	if len(routeRes.ObservedOverflowModels) > 0 {
		ctx = context.WithValue(ctx, ObservedOverflowModelsContextKey{}, routeRes.ObservedOverflowModels)
	}
	if len(routeRes.SessionDemotedModels) > 0 {
		ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, routeRes.SessionDemotedModels)
		if len(routeRes.SessionCooldownModels) > 0 {
			ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, routeRes.SessionCooldownModels)
		}
		if len(routeRes.SessionStrikeReadmitModels) > 0 {
			ctx = context.WithValue(ctx, SessionStrikeReadmitModelsContextKey{}, routeRes.SessionStrikeReadmitModels)
		}
	}
	if len(routeRes.CooldownProbeDeniedModels) > 0 {
		ctx = context.WithValue(ctx, SessionCooldownProbeDeniedModelsContextKey{}, routeRes.CooldownProbeDeniedModels)
	}
	return ctx
}
