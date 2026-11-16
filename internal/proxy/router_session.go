package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

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
