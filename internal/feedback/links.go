package feedback

import (
	"context"
	"errors"
	"strings"
	"time"

	"weave-os/router/internal/telemetry"

	"github.com/google/uuid"
)

const routerFeedbackSpanName = "router.feedback"

const feedbackSourceLink = "link"

var ErrFeedbackUnavailable = errors.New("proxy: feedback repository not configured")

type Links struct {
	repo    FeedbackRepository
	signer  *Signer
	baseURL string
	emitter telemetry.Emitter
	now     func() time.Time
}

func NewLinks(repo FeedbackRepository, signer *Signer, baseURL string, emitter telemetry.Emitter, now func() time.Time) *Links {
	return &Links{repo: repo, signer: signer, baseURL: strings.TrimRight(baseURL, "/"), emitter: emitter, now: now}
}

func (l *Links) Enabled() bool {
	return l != nil && l.signer != nil
}

func (l *Links) Repository() FeedbackRepository {
	if l == nil {
		return nil
	}
	return l.repo
}

func (l *Links) BaseURL() string {
	if l == nil {
		return ""
	}
	return l.baseURL
}

func (l *Links) VerifyFeedbackToken(token string) (Claims, error) {
	return l.signer.Verify(token)
}

func (l *Links) GetFeedbackContext(ctx context.Context, installationID, requestID string) (FeedbackContext, error) {
	if l.repo == nil {
		return FeedbackContext{}, ErrFeedbackUnavailable
	}
	return l.repo.GetContext(ctx, installationID, requestID)
}

func (l *Links) SubmitFeedback(ctx context.Context, p SubmitFeedbackParams) error {
	if l.repo == nil {
		return ErrFeedbackUnavailable
	}
	err := l.repo.Upsert(ctx, UpsertFeedbackParams{
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
	l.emitFeedbackSpan(ctx, p)
	return nil
}

func (l *Links) emitFeedbackSpan(ctx context.Context, p SubmitFeedbackParams) {
	if l.emitter == nil {
		return
	}
	now := l.now()
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
	ctx = telemetry.WithRecorder(context.WithoutCancel(ctx), l.emitter.NewRecorder())
	telemetry.Record(ctx, telemetry.Span{Name: routerFeedbackSpanName, Start: now, End: now, Attrs: b.Build()})
	telemetry.Flush(ctx)
}

func (l *Links) MintToken(installationID uuid.UUID, externalID, requestID, routerUserID string) string {
	if l == nil || l.signer == nil || l.baseURL == "" {
		return ""
	}
	if installationID == uuid.Nil || externalID == "" || requestID == "" {
		return ""
	}
	return l.signer.Mint(installationID.String(), externalID, requestID, routerUserID)
}
