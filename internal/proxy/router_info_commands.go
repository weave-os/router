package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

type routerInfoCommands struct {
	s *Service
}

func (c routerInfoCommands) session(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	identity := ClientIdentityFrom(ctx)
	ack := newCommandAcknowledgment(translate.CommandRouterSession)
	ack.sessionID, ack.clientApp = identity.SessionID, identity.ClientApp

	observability.FromContext(ctx).Debug("/router-session: answered from request identity",
		"has_session_id", identity.SessionID != "",
		"client_app", identity.ClientApp,
	)

	return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
}

func (c routerInfoCommands) models(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	log := observability.FromContext(ctx)
	routable := c.s.RoutableModels()
	excluded := c.s.excludedModelsForRequest(ctx)
	automaticExcluded := c.s.globalAutomaticExcludedModels(ctx)

	log.Debug("/router-models: answered from the request's own routing view",
		"routable", len(routable),
		"excluded", len(excluded),
		"automatic_excluded", len(automaticExcluded),
	)

	ack := newCommandAcknowledgment(translate.CommandRouterModels)
	ack.routable, ack.excluded, ack.automaticExcluded = routable, excluded, automaticExcluded
	ack.clientApp = ClientIdentityFrom(ctx).ClientApp
	return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
}

func (c routerInfoCommands) listModels(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	models := conversation.ListForceableModels(ctx, forceModelNames(), forceModelResolver(c))
	ack := newCommandAcknowledgment(translate.CommandListModels)
	ack.forceable = models
	ack.clientApp = ClientIdentityFrom(ctx).ClientApp
	return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
}
