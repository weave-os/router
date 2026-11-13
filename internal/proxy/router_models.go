package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

// handleRouterModelsCommand answers a bare router-models directive
// synthetically, with no upstream call.
//
// This reports strictly more than the skill it replaces. The skill shells out
// to `weave-router models`, which on a managed router gets a 404 from the
// admin API and degrades to the unauthenticated catalog -- a flat list that
// has to tell the user "this router does not report which of them your
// installation has enabled". The exclusions ARE on this request, so the
// router can mark each row instead of disclaiming the question.
func (s *Service) handleRouterModelsCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	log := observability.FromContext(ctx)
	routable := s.RoutableModels()
	excluded := s.excludedModelsForRequest(ctx)
	// Deployment-wide automatic exclusions are a separate, softer set: the
	// router may not CHOOSE these, but an explicit force still serves them.
	// Folding them into `excluded` would claim they are unusable; leaving
	// them out would claim they are routable. They get their own marker.
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
