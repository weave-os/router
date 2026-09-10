package proxy

import (
	"context"
	"fmt"
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
	msg := routerSessionMessage(identity.SessionID, identity.ClientApp, env.SourceFormat())

	log.Debug("/router-session: answered from request identity",
		"has_session_id", identity.SessionID != "",
		"client_app", identity.ClientApp,
	)

	if env.SourceFormat() == translate.FormatOpenAI {
		return writeSyntheticOpenAIResponse(w, env, msg, inputTokens)
	}
	return writeSyntheticAnthropicResponse(w, env, msg, inputTokens)
}

// routerSessionMessage renders the ack in the marker style each surface uses:
// OpenAI-format clients (Codex) get plain text, Anthropic-format clients get
// the ✦ marker the rest of the router's synthetic replies use there.
func routerSessionMessage(sessionID, clientApp string, format translate.Format) string {
	if sessionID == "" {
		// No session header means nothing here is correlated, so naming a
		// substitute id would point support at the wrong session. Say so.
		const detail = "no session id on this request. This client did not send one, so its turns are not correlated in router telemetry."
		if format == translate.FormatOpenAI {
			return "Weave Router: " + detail
		}
		return "✦ **Weave Router** → " + detail + "\n\n"
	}
	hint := directivePrefix(clientApp) + "router-feedback"
	if format == translate.FormatOpenAI {
		return fmt.Sprintf("Weave Router: session id: %s. Quote it in support requests; %s attaches feedback to this session.", sessionID, hint)
	}
	return fmt.Sprintf("✦ **Weave Router** → session id: `%s` · quote it in support requests; `%s` attaches feedback to this session\n\n", sessionID, hint)
}
