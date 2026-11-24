package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"

	"github.com/google/uuid"
)

const HeaderRouterFeedbackURL = "x-router-feedback-url"

func (s *Service) setFeedbackLinkHeader(ctx context.Context, w http.ResponseWriter, installationID uuid.UUID, externalID, requestID, routerUserID string) {
	if hideTerminalSurfacesForRequest(ctx) {
		return
	}
	token := s.lifecycle.FeedbackLinks().MintToken(installationID, externalID, requestID, routerUserID)
	if token == "" {
		return
	}
	w.Header().Set(HeaderRouterFeedbackURL, s.lifecycle.FeedbackLinks().BaseURL()+"/f/"+token)
}

var terminalFeedbackClients = map[string]struct{}{
	requestcontext.ClientAppClaudeCode: {},
	requestcontext.ClientAppCodex:      {},
	requestcontext.ClientAppOpencode:   {},
}

const feedbackFooterText = "\n\n_Weave Router feedback:_ `/rf +` good experience · `/rf -` poor experience · note optional, e.g. `/rf - too slow`"

const feedbackFooterTextCodex = "\n\n_Weave Router feedback:_ `$rf +` good experience · `$rf -` poor experience · note optional, e.g. `$rf - too slow`"

func (s *Service) feedbackFooter(ctx context.Context, clientApp string, tt turntype.TurnType, echoedInConversation bool) string {
	if hideTerminalSurfacesForRequest(ctx) {
		return ""
	}
	if s.lifecycle.RouterFeedbackStore() == nil {
		return ""
	}
	if _, ok := terminalFeedbackClients[clientApp]; !ok {
		return ""
	}
	if tt != turntype.MainLoop && tt != turntype.ToolResult {
		return ""
	}
	if echoedInConversation || isHeadlessClientUserAgent(requestcontext.ClientIdentityFrom(ctx).UserAgent) {
		return ""
	}
	if clientApp == requestcontext.ClientAppCodex {
		return feedbackFooterTextCodex
	}
	return feedbackFooterText
}

func isHeadlessClientUserAgent(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	return strings.Contains(ua, ", sdk-") || strings.HasPrefix(ua, "codex_exec") || strings.HasPrefix(ua, "codex_sdk")
}
