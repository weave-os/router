package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"weave-os/router/internal/feedback"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/telemetry"

	"github.com/google/uuid"
)

const HeaderRouterFeedbackURL = "x-router-feedback-url"

const routerFeedbackSpanName = "router.feedback"

const feedbackSourceLink = "link"

var ErrFeedbackUnavailable = errors.New("proxy: feedback repository not configured")

type feedbackLinks struct {
	repo    feedback.FeedbackRepository
	signer  *feedback.Signer
	baseURL string
}

func (s *Service) WithFeedback(repo feedback.FeedbackRepository, signer *feedback.Signer, baseURL string) *Service {
	s.feedbackLinks = feedbackLinks{repo: repo, signer: signer, baseURL: strings.TrimRight(baseURL, "/")}
	return s
}

func (s *Service) FeedbackEnabled() bool {
	return s != nil && s.feedbackLinks.signer != nil
}

func (s *Service) VerifyFeedbackToken(token string) (feedback.Claims, error) {
	return s.feedbackLinks.signer.Verify(token)
}

func (s *Service) GetFeedbackContext(ctx context.Context, installationID, requestID string) (feedback.FeedbackContext, error) {
	if s.feedbackLinks.repo == nil {
		return feedback.FeedbackContext{}, ErrFeedbackUnavailable
	}
	return s.feedbackLinks.repo.GetContext(ctx, installationID, requestID)
}

func (s *Service) SubmitFeedback(ctx context.Context, p feedback.SubmitFeedbackParams) error {
	if s.feedbackLinks.repo == nil {
		return ErrFeedbackUnavailable
	}
	err := s.feedbackLinks.repo.Upsert(ctx, feedback.UpsertFeedbackParams{
		InstallationID: p.InstallationID,
		ExternalID:     p.ExternalID,
		RequestID:      p.RequestID,
		Rating:         p.Rating,
		Comment:        p.Comment,
		Source:         feedbackSourceLink,
		RouterUserID:   p.RouterUserID,
	})
	if err != nil {
		return err
	}
	s.emitFeedbackSpan(ctx, p)
	return nil
}

func (s *Service) emitFeedbackSpan(ctx context.Context, p feedback.SubmitFeedbackParams) {
	if s.turnTelemetry.emitter == nil {
		return
	}
	now := s.clock.Now()
	b := telemetry.NewAttrBuilder(6).
		String("external_id", p.ExternalID).
		String("request_id", p.RequestID).
		String("feedback.rating", p.Rating).
		String("feedback.source", feedbackSourceLink)
	if p.Comment != nil {
		b = b.String("feedback.comment", *p.Comment)
	}
	if p.RouterUserID != "" {
		b = b.String("router_user_id", p.RouterUserID)
	}
	ctx = telemetry.WithRecorder(context.WithoutCancel(ctx), s.newTelemetryRecorder())
	telemetry.Record(ctx, telemetry.Span{Name: routerFeedbackSpanName, Start: now, End: now, Attrs: b.Build()})
	telemetry.Flush(ctx)
}

func (l feedbackLinks) mintToken(installationID uuid.UUID, externalID, requestID, routerUserID string) string {
	if l.signer == nil || l.baseURL == "" {
		return ""
	}
	if installationID == uuid.Nil || externalID == "" || requestID == "" {
		return ""
	}
	return l.signer.Mint(installationID.String(), externalID, requestID, routerUserID)
}

func (s *Service) setFeedbackLinkHeader(ctx context.Context, w http.ResponseWriter, installationID uuid.UUID, externalID, requestID, routerUserID string) {
	if hideTerminalSurfacesForRequest(ctx) {
		return
	}
	token := s.feedbackLinks.mintToken(installationID, externalID, requestID, routerUserID)
	if token == "" {
		return
	}
	w.Header().Set(HeaderRouterFeedbackURL, s.feedbackLinks.baseURL+"/f/"+token)
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
	if echoedInConversation || isHeadlessClientUserAgent(ClientIdentityFrom(ctx).UserAgent) {
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
