package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/conversation"
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

	conversation.LogRouterSession(ctx, identity.SessionID, identity.ClientApp)

	return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
}

func (c routerInfoCommands) models(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	view := conversation.ReadRouterModels(ctx, c)

	ack := newCommandAcknowledgment(translate.CommandRouterModels)
	ack.routable, ack.excluded, ack.automaticExcluded = view.Routable, view.Excluded, view.AutomaticExcluded
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

func (c routerInfoCommands) RoutableModels() map[string]struct{} { return c.s.RoutableModels() }

func (c routerInfoCommands) ExcludedModels(ctx context.Context) map[string]struct{} {
	return c.s.excludedModelsForRequest(ctx)
}

func (c routerInfoCommands) AutomaticExcludedModels(ctx context.Context) map[string]struct{} {
	return c.s.globalAutomaticExcludedModels(ctx)
}
