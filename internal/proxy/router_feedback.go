package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

// routerFeedbackCommandSpanName is the OTLP span for the /router-feedback (/rf)
// slash command. Distinct from routerFeedbackSpanName ("router.feedback" in
// feedback.go), which is a downstream contract (buildFeedbackRow); do not reuse
// that name or alter its schema.
const routerFeedbackCommandSpanName = "router.feedback.command"

// routerFeedbackAcceptTimeout bounds the acceptance transaction. It runs on a
// context detached from the request so a client disconnect mid-command cannot
// drop feedback the user explicitly typed.
const routerFeedbackAcceptTimeout = 5 * time.Second

// RouterFeedbackEvent mirrors one router.router_feedback row.
type RouterFeedbackEvent struct {
	ID              string
	ExternalID      string
	Sequence        int
	TargetSequence  int64
	Strategy        string
	ServedProvider  string
	RolloutID       string
	TrainingAllowed bool
	DeliveryStatus  string
	Attempts        int
	LeaseToken      string
	LastError       string
	CreatedAt       time.Time
	InstallationID  string
	SessionKey      []byte
	Role            string
	RouterUserID    string
	ClientApp       string
	SessionID       string
	RequestedModel  string
	ServedModel     string
	// Rating is the thumbs verdict ("up", "down", or "" for note-only),
	// parsed from /rf+ /rf- or a leading verdict token in the note.
	Rating string
	// SuggestedLabel is the complexity label from a --label flag ("fast" | "explore" | "balanced" | "high" | "maximum").
	SuggestedLabel string
	// Feedback is the persisted submission text; verdict-only submissions
	// get a compact emoji so the column is never empty.
	Feedback string
	// Source is how the feedback was submitted: "user" (explicit /rf command)
	// or "auto" (automated judge at session stop).
	Source string
	// RequestID is the exact completed request, or empty for an unattached submission.
	RequestID string
	// RouteID is the rated request's saved sidecar join key, when available.
	RouteID string
}

// Attached reports whether acceptance resolved the selector to a completed
// request. An unattached event is still saved; it never reports to a policy.
func (e RouterFeedbackEvent) Attached() bool {
	return e.RequestID != ""
}

// RouterFeedbackSource values for validated event persistence.
const (
	RouterFeedbackSourceUser = "user"
	RouterFeedbackSourceAuto = "auto"
)

// handleRouterFeedbackCommand accepts a /router-feedback submission in one
// store transaction, then emits a router.feedback.command span. User-issued
// commands receive a synthetic acknowledgment; agent-issued tool-result
// commands continue through routing. Policy reporting happens later from the
// durable row, never here.
func (s *Service) handleRouterFeedbackCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	cmd translate.RouterFeedbackResult,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	inputTokens int,
	synthetic bool,
) error {
	log := observability.FromContext(ctx)
	role := roleForTier(catalog.TierFor(env.Model()))
	rf := directivePrefix(ClientIdentityFrom(ctx).ClientApp) + "rf"

	feedback := strings.TrimSpace(cmd.Feedback)
	rating := cmd.Rating
	if rating == "" && feedback == "" {
		// No verdict and no note. Message is formatted as a routing marker so
		// StripRoutingMarkerFromMessages strips it from later requests.
		msg := fmt.Sprintf("✦ **Weave Router** → Router-feedback needs a verdict or a note, e.g. %s+ or %s- too slow.\n\n", rf, rf)
		if env.SourceFormat() == translate.FormatOpenAI {
			msg = fmt.Sprintf("Weave Router: router-feedback needs a verdict or a note, e.g. %s+ or %s- too slow.", rf, rf)
		}
		if synthetic {
			return writeSyntheticCommandResponse(w, env, msg, inputTokens)
		}
		return nil
	}
	if s.feedbackStore == nil || installationID == uuid.Nil {
		return ErrFeedbackUnavailable
	}

	clientID := ClientIdentityFrom(ctx)
	routerUserID := auth.UserIDFrom(ctx)
	externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	rolloutID := clientID.RolloutID
	if persistedRolloutID, ok := ctx.Value(PolicyRolloutIDContextKey{}).(string); ok && persistedRolloutID != "" {
		rolloutID = persistedRolloutID
	}
	selector := cmd.Sequence
	if selector == 0 {
		selector = -1
	}

	// The command UUID is the durable delivery identity; it is fixed before
	// the transaction so a retried acceptance cannot mint a second event.
	submission := RouterFeedbackEvent{
		ID:              uuid.NewString(),
		ExternalID:      externalID,
		Sequence:        selector,
		RolloutID:       rolloutID,
		TrainingAllowed: policyTrainingAllowedForRequest(ctx),
		DeliveryStatus:  RouterFeedbackPending,
		InstallationID:  installationID.String(),
		SessionKey:      sessionKey[:],
		Role:            role,
		RouterUserID:    routerUserID,
		ClientApp:       clientID.ClientApp,
		SessionID:       clientID.SessionID,
		RequestedModel:  env.Model(),
		Rating:          rating,
		SuggestedLabel:  cmd.SuggestedLabel,
		Feedback:        persistedFeedbackText(rating, feedback),
		Source:          RouterFeedbackSourceUser,
	}
	acceptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routerFeedbackAcceptTimeout)
	accepted, err := s.feedbackStore.AcceptRouterFeedback(acceptCtx, submission)
	cancel()
	if err != nil {
		log.Error("Router feedback acceptance failed", "feedback_id", submission.ID, "selector", selector, "err", err)
		return fmt.Errorf("accept router feedback: %w", err)
	}

	now := time.Now()
	attrs := otel.NewAttrBuilder(17).
		String("external_id", externalID).
		String("router_user_id", routerUserID).
		String("client.device_id", clientID.DeviceID).
		String("client.session_id", clientID.SessionID).
		String("client.user_agent", clientID.UserAgent).
		String("client.app", clientID.ClientApp).
		String("requested.model", env.Model()).
		String("feedback.served_model", accepted.ServedModel).
		String("feedback.role", role).
		String("feedback.rating", rating).
		Int64("feedback.sequence", int64(selector)).
		String("feedback.text", feedback).
		String("feedback.source", RouterFeedbackSourceUser).
		String("feedback.id", accepted.ID)
	if accepted.Attached() {
		attrs = attrs.String("feedback.request_id", accepted.RequestID).Int64("feedback.target_sequence", accepted.TargetSequence)
	}
	if accepted.RouteID != "" {
		attrs = attrs.String("feedback.route_id", accepted.RouteID)
	}
	otel.Record(ctx, otel.Span{
		Name:  routerFeedbackCommandSpanName,
		Start: now,
		End:   now,
		Attrs: attrs.Build(),
	})

	log.Info("Router feedback accepted",
		"feedback_id", accepted.ID,
		"rating", rating,
		"served_model", accepted.ServedModel,
		"requested_model", env.Model(),
		"role", role,
		"selector", selector,
		"attached", accepted.Attached(),
		"target_sequence", accepted.TargetSequence,
		"rated_request_id", accepted.RequestID,
		"route_id", accepted.RouteID,
		"strategy", accepted.Strategy,
		"delivery_status", accepted.DeliveryStatus,
	)

	if synthetic {
		return writeSyntheticCommandResponse(w, env, routerFeedbackAck(env.SourceFormat(), rating, accepted.Attached(), rf), inputTokens)
	}
	return nil
}

// routerFeedbackAck renders the acknowledgment, echoing the verdict. An
// unattached save says so rather than pretending the selector resolved. The
// Anthropic-format ack is wrapped as a routing marker so it gets stripped
// from subsequent turns.
func routerFeedbackAck(format translate.Format, rating string, attached bool, rf string) string {
	verdict := ""
	switch rating {
	case translate.RouterFeedbackRatingUp:
		verdict = " 👍"
	case translate.RouterFeedbackRatingDown:
		verdict = " 👎"
	}
	body := "Feedback saved" + verdict + ". Thank you."
	if !attached {
		body = fmt.Sprintf("Feedback saved%s, but no completed response matched that number, so it is saved unattached. Try `%s` without a number for the last response.", verdict, rf)
	}
	if format == translate.FormatOpenAI {
		return "Weave Router: " + body
	}
	return "✦ **Weave Router** → " + body + "\n\n"
}

// persistedFeedbackText is the value written to router.router_feedback.feedback.
// Verdict-only submissions get a compact emoji so the NOT NULL column is never empty.
func persistedFeedbackText(rating, feedback string) string {
	if feedback != "" {
		return feedback
	}
	switch rating {
	case translate.RouterFeedbackRatingUp:
		return "👍"
	case translate.RouterFeedbackRatingDown:
		return "👎"
	}
	return ""
}

// writeSyntheticCommandResponse writes a router-command acknowledgment in the
// inbound wire format without dispatching upstream.
func writeSyntheticCommandResponse(w http.ResponseWriter, env *translate.RequestEnvelope, msg string, inputTokens int) error {
	switch env.SourceFormat() {
	case translate.FormatOpenAI:
		return writeSyntheticOpenAIResponse(w, env, msg, inputTokens)
	default:
		return writeSyntheticAnthropicResponse(w, env, msg, inputTokens)
	}
}
