package proxy

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

const routerFeedbackCommandSpanName = "router.feedback.command"

type RouterFeedbackStore interface {
	InsertRouterFeedback(ctx context.Context, p RouterFeedbackEvent) error
}

type RouterFeedbackEvent struct {
	InstallationID string
	SessionKey     []byte
	Role           string
	RouterUserID   string
	ClientApp      string
	SessionID      string
	RequestedModel string
	ServedModel    string
	Rating         string
	SuggestedLabel string
	Feedback       string
	Source         string
	RequestID      string
	RouteID        string
}

const (
	RouterFeedbackSourceUser = "user"
	RouterFeedbackSourceAuto = "auto"
)

func (s *Service) handleRouterFeedbackCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	cmd translate.RouterFeedbackResult,
	target sessionTarget,
	inputTokens int,
	synthetic bool,
) error {
	log := observability.FromContext(ctx)
	role := roleForTier(catalog.TierFor(env.Model()))

	ack := newCommandAcknowledgment(translate.CommandRouterFeedback)
	ack.clientApp = ClientIdentityFrom(ctx).ClientApp
	feedback := strings.TrimSpace(cmd.Feedback)
	rating := cmd.Rating
	if rating == "" && feedback == "" {
		ack.feedback = feedbackEmpty
		ack.outcome.Disposition = translate.CommandRejected
		if synthetic {
			return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
		}
		return nil
	}

	var servedModel string
	var telemetryRequestID string
	var telemetryRouteID string
	var telemetryStrategy string
	if cmd.Sequence != 0 && s.turnTelemetry.repo != nil {
		turn, err := s.turnTelemetry.repo.GetTelemetryBySessionSequence(ctx, target.installationID, target.sessionKey[:], role, cmd.Sequence)
		if err != nil {
			log.Error("/router-feedback: sequence lookup failed", "sequence", cmd.Sequence, "err", err)
			if errors.Is(err, sql.ErrNoRows) {
				ack.feedback = feedbackMissingSequence
				ack.outcome.Disposition = translate.CommandRejected
				if synthetic {
					return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
				}
				return nil
			}
		} else {
			servedModel = turn.DecisionModel
			telemetryRequestID = turn.RequestID
			telemetryRouteID = turn.RouteID
			telemetryStrategy = turn.Strategy
			log.Info("/router-feedback: resolved sequence to telemetry turn",
				"sequence", cmd.Sequence,
				"rated_request_id", telemetryRequestID,
				"served_model", servedModel,
			)
		}
	}
	if servedModel == "" && s.pinStore != nil {
		pin := sessionpin.Pin{}
		if storedPin, found, err := s.pinStore.Get(ctx, target.sessionKey, role); err != nil {
			log.Error("/router-feedback: pin lookup failed", "err", err)
		} else if found && pinMatchesEffectiveStrategy(target.strategy, storedPin) {
			pin = storedPin
		}
		forceHistory := s.loadForceModelHistory(ctx, target.sessionKey, role)
		servedModel, _ = switchHistoryFromPins(pin, forceHistory)
		if servedModel == "" {
			servedModel = pin.Model
		}
	}

	clientID := ClientIdentityFrom(ctx)
	routerUserID := auth.UserIDFrom(ctx)
	externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)

	if s.feedbackStore != nil && target.installationID != uuid.Nil {
		event := RouterFeedbackEvent{
			InstallationID: target.installationID.String(),
			SessionKey:     target.sessionKey[:],
			Role:           role,
			RouterUserID:   routerUserID,
			ClientApp:      clientID.TelemetryClientApp(),
			SessionID:      clientID.SessionID,
			RequestedModel: env.Model(),
			ServedModel:    servedModel,
			Rating:         rating,
			SuggestedLabel: cmd.SuggestedLabel,
			Feedback:       persistedFeedbackText(rating, feedback),
			Source:         RouterFeedbackSourceUser,
			RequestID:      telemetryRequestID,
			RouteID:        telemetryRouteID,
		}
		if err := s.feedbackStore.InsertRouterFeedback(context.WithoutCancel(ctx), event); err != nil {
			log.Error("/router-feedback: feedback insert failed", "err", err)
			return err
		}
	}
	if telemetryRequestID != "" && rating != "" && s.feedbackRepo != nil {
		comment := feedback
		upsertParams := UpsertFeedbackParams{
			InstallationID: target.installationID.String(),
			ExternalID:     externalID,
			RequestID:      telemetryRequestID,
			Rating:         rating,
			Comment:        &comment,
			Source:         "router-feedback-command",
			RouterUserID:   routerUserID,
		}
		if err := s.feedbackRepo.Upsert(context.WithoutCancel(ctx), upsertParams); err != nil {
			log.Error("/router-feedback: request_feedback upsert failed", "rated_request_id", telemetryRequestID, "err", err)
		}
	}

	if telemetryStrategy != "" {
		target.strategy = router.Strategy(telemetryStrategy)
	}
	if registered, ok := s.strategies[target.strategy]; ok && registered.feedback != nil {
		trainingAllowed := policyTrainingAllowedForRequest(ctx)
		trainingDelta := []router.ConversationMessage(nil)
		if (cmd.Sequence == 0 || cmd.Sequence == -1) && s.strategyPorts.Ownership.Owns(target.strategy) && trainingAllowed {
			trainingDelta = routerFeedbackTrainingDelta(env)
		}
		s.reportRouterFeedback(ctx, registered.feedback, target.strategy, externalID, target.installationID, target.sessionKey, role, routerUserID, clientID, env.Model(), servedModel, rating, feedback, cmd.SuggestedLabel, RouterFeedbackSourceUser, trainingDelta, telemetryRequestID, telemetryRouteID)
	}

	now := s.clock.Now()
	attrs := telemetry.NewAttrBuilder(15).
		String("external_id", externalID).
		String("router_user_id", routerUserID).
		String("client.device_id", clientID.DeviceID).
		String("client.session_id", clientID.SessionID).
		String("client.user_agent", clientID.UserAgent).
		String("client.app", clientID.TelemetryClientApp()).
		String("requested.model", env.Model()).
		String("feedback.served_model", servedModel).
		String("feedback.role", role).
		String("feedback.rating", rating).
		Int64("feedback.sequence", int64(cmd.Sequence)).
		String("feedback.text", feedback).
		String("feedback.source", RouterFeedbackSourceUser)
	if telemetryRequestID != "" {
		attrs = attrs.String("feedback.request_id", telemetryRequestID)
	}
	if telemetryRouteID != "" {
		attrs = attrs.String("feedback.route_id", telemetryRouteID)
	}
	telemetry.Record(ctx, telemetry.Span{
		Name:  routerFeedbackCommandSpanName,
		Start: now,
		End:   now,
		Attrs: attrs.Build(),
	})
	telemetry.Flush(ctx)

	log.Info("router.feedback.command",
		"rating", rating,
		"feedback", feedback,
		"served_model", servedModel,
		"requested_model", env.Model(),
		"role", role,
		"sequence", cmd.Sequence,
		"rated_request_id", telemetryRequestID,
		"route_id", telemetryRouteID,
	)

	if synthetic {
		ack.feedback, ack.rating = feedbackRecorded, rating
		return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
	}
	return nil
}

func (s *Service) reportRouterFeedback(
	ctx context.Context,
	reporter policy.FeedbackReporter,
	strategy router.Strategy,
	organizationID string,
	installationID uuid.UUID,
	sessionKey [sessionpin.SessionKeyLen]byte,
	role string,
	routerUserID string,
	clientID ClientIdentity,
	requestedModel string,
	servedModel string,
	rating string,
	feedback string,
	suggestedLabel string,
	source string,
	trainingDelta []router.ConversationMessage,
	requestID string,
	routeID string,
) {
	payload := map[string]interface{}{
		"strategy":          string(strategy),
		"feedback_key":      hex.EncodeToString(sessionKey[:]),
		"feedback_role":     role,
		"rating":            rating,
		"feedback":          feedback,
		"requested_model":   requestedModel,
		"served_model":      servedModel,
		"router_user_id":    routerUserID,
		"client_app":        clientID.TelemetryClientApp(),
		"client_session_id": clientID.SessionID,
		"source":            source,
		"request_id":        requestID,
		"route_id":          routeID,
	}
	if suggestedLabel != "" {
		payload["suggested_label"] = suggestedLabel
	}
	if organizationID != "" {
		payload["organization_id"] = organizationID
	}
	rolloutID := clientID.RolloutID
	if persistedRolloutID, ok := ctx.Value(PolicyRolloutIDContextKey{}).(string); ok && persistedRolloutID != "" {
		rolloutID = persistedRolloutID
	}
	payload["rollout_id"] = rolloutID
	trainingAllowed := policyTrainingAllowedForRequest(ctx)
	payload["training_allowed"] = trainingAllowed
	if installationID != uuid.Nil {
		payload["installation_id"] = installationID.String()
	}
	if s.strategyPorts.Ownership.Owns(strategy) && trainingAllowed && len(trainingDelta) > 0 {
		payload["training_conversation_delta"] = trainingDelta
	}
	log := observability.FromContext(ctx)
	if identity, managed := requestcontext.ServingIdentityFromContext(ctx); managed {
		payload["serving_identity"] = identity
	}
	observability.SafeGoContext(ctx, log, policyFeedbackReportTimeout, "reportPolicyFeedback", func(reportCtx context.Context) {
		if err := reporter.ReportFeedback(reportCtx, payload); err != nil {
			log.Error("/router-feedback: policy feedback report failed", "strategy", strategy, "err", err)
		}
	})
}

func routerFeedbackTrainingDelta(env *translate.RequestEnvelope) []router.ConversationMessage {
	messages := conversationMessagesForRouting(env)
	if len(messages) == 0 {
		return nil
	}
	ratedAssistant := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "assistant") {
			ratedAssistant = i
			break
		}
	}
	if ratedAssistant < 0 {
		return nil
	}
	start := 0
	for i := ratedAssistant - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "assistant") {
			start = i + 1
			break
		}
	}
	if start == 0 {
		for start < ratedAssistant && !strings.EqualFold(strings.TrimSpace(messages[start].Role), "user") {
			start++
		}
	}
	return append([]router.ConversationMessage(nil), messages[start:ratedAssistant+1]...)
}

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
