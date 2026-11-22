package ingress

import (
	"net/http"
	"strings"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

func WriteRetiredBetaRequest(w http.ResponseWriter, r *http.Request, body []byte) (bool, error) {
	var surface requestcontext.ConversationSurface
	switch r.URL.Path {
	case "/v1/messages", "/v1/route", "/v1/route/preview", "/v1/route/handoff":
		surface = requestcontext.ConversationAnthropic
	case "/v1/chat/completions":
		surface = requestcontext.ConversationChat
	case "/v1/responses":
		surface = requestcontext.ConversationResponses
	default:
		if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
			surface = requestcontext.ConversationGemini
		} else {
			return false, nil
		}
	}
	return translate.WriteRetiredBetaRequest(w, r, body, surface)
}
