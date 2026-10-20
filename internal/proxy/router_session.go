package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

// handleRouterSessionCommand answers a router-session directive synthetically,
// with no upstream call.
//
// The id reported is ClientIdentity.SessionID -- the same value the router
// stores on telemetry and keys session cost lookups by -- so it is the id that
// actually resolves in the dashboard. Reading it here rather than in a client
// skill also removes the model turn the skill needed: the directive now costs
// zero inference, like force-model and router-feedback.
func (s *Service) handleRouterSessionCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	log := observability.FromContext(ctx)
	identity := ClientIdentityFrom(ctx)
	ack := newCommandAcknowledgment(translate.CommandRouterSession)
	ack.sessionID, ack.clientApp = identity.SessionID, identity.ClientApp

	log.Debug("/router-session: answered from request identity",
		"has_session_id", identity.SessionID != "",
		"client_app", identity.ClientApp,
	)

	return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
}
