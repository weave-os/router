package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

func (s *Service) handleRouterModelsCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	log := observability.FromContext(ctx)
	routable := s.RoutableModels()
	excluded := s.excludedModelsForRequest(ctx)
	automaticExcluded := s.globalAutomaticExcludedModels(ctx)

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
