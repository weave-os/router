package conversation

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
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

const RouterFeedbackSourceUser = "user"

type FeedbackTurn struct {
	DecisionModel string
	RequestID     string
	RouteID       string
	Strategy      string
}

type FeedbackTurns interface {
	TurnBySequence(ctx context.Context, installationID uuid.UUID, sessionKey []byte, role string, sequence int) (turn FeedbackTurn, notFound bool, err error)
}

type FeedbackServedModels interface {
	ServedModel(ctx context.Context, strategy router.Strategy, sessionKey [sessionpin.SessionKeyLen]byte, role string) string
}

type RequestFeedback struct {
	InstallationID string
	ExternalID     string
	RequestID      string
	Rating         string
	Comment        string
	Source         string
	RouterUserID   string
}

type RequestFeedbackStore interface {
	UpsertRequestFeedback(ctx context.Context, feedback RequestFeedback) error
}

type FeedbackReporters interface {
	Reporter(strategy router.Strategy) (policy.FeedbackReporter, bool)
	Owns(strategy router.Strategy) bool
}

type RouterFeedback struct {
	Turns         FeedbackTurns
	ServedModels  FeedbackServedModels
	Store         RouterFeedbackStore
	Requests      RequestFeedbackStore
	Reporters     FeedbackReporters
	Now           func() time.Time
	ReportTimeout time.Duration
}

type RouterFeedbackRequest struct {
	Command         translate.RouterFeedbackResult
	InstallationID  uuid.UUID
	SessionKey      [sessionpin.SessionKeyLen]byte
	Strategy        router.Strategy
	Role            string
	RequestedModel  string
	Client          requestcontext.ClientIdentity
	RouterUserID    string
	ExternalID      string
	RolloutID       string
	TrainingAllowed bool
	TrainingDelta   func() []router.ConversationMessage
}

type RouterFeedbackResult string

const (
	RouterFeedbackRecorded        RouterFeedbackResult = "recorded"
	RouterFeedbackEmpty           RouterFeedbackResult = "empty"
	RouterFeedbackMissingSequence RouterFeedbackResult = "missing_sequence"
)

type ratedFeedback struct {
	RouterFeedbackRequest
	rating      string
	feedback    string
	servedModel string
	turn        FeedbackTurn
}

func (f RouterFeedback) Record(ctx context.Context, req RouterFeedbackRequest) (RouterFeedbackResult, error) {
	rated := ratedFeedback{RouterFeedbackRequest: req, rating: req.Command.Rating, feedback: strings.TrimSpace(req.Command.Feedback)}
	if rated.rating == "" && rated.feedback == "" {
		return RouterFeedbackEmpty, nil
	}
	turn, missing := f.ratedTurn(ctx, req)
	if missing {
		return RouterFeedbackMissingSequence, nil
	}
	rated.turn = turn
	rated.servedModel = turn.DecisionModel
	if rated.servedModel == "" && f.ServedModels != nil {
		rated.servedModel = f.ServedModels.ServedModel(ctx, req.Strategy, req.SessionKey, req.Role)
	}
	if err := f.persist(ctx, rated); err != nil {
		return "", err
	}
	f.upsertRequestFeedback(ctx, rated)
	f.reportToStrategy(ctx, rated)
	f.recordSpan(ctx, rated)
	observability.FromContext(ctx).Info("router.feedback.command",
		"rating", rated.rating,
		"feedback", rated.feedback,
		"served_model", rated.servedModel,
		"requested_model", req.RequestedModel,
		"role", req.Role,
		"sequence", req.Command.Sequence,
		"rated_request_id", turn.RequestID,
		"route_id", turn.RouteID,
	)
	return RouterFeedbackRecorded, nil
}

func (f RouterFeedback) ratedTurn(ctx context.Context, req RouterFeedbackRequest) (FeedbackTurn, bool) {
	if req.Command.Sequence == 0 || f.Turns == nil {
		return FeedbackTurn{}, false
	}
	log := observability.FromContext(ctx)
	turn, notFound, err := f.Turns.TurnBySequence(ctx, req.InstallationID, req.SessionKey[:], req.Role, req.Command.Sequence)
	if err != nil {
		log.Error("/router-feedback: sequence lookup failed", "sequence", req.Command.Sequence, "err", err)
		return FeedbackTurn{}, notFound
	}
	log.Info("/router-feedback: resolved sequence to telemetry turn",
		"sequence", req.Command.Sequence,
		"rated_request_id", turn.RequestID,
		"served_model", turn.DecisionModel,
	)
	return turn, false
}

func (f RouterFeedback) persist(ctx context.Context, rated ratedFeedback) error {
	if f.Store == nil || rated.InstallationID == uuid.Nil {
		return nil
	}
	event := RouterFeedbackEvent{
		InstallationID: rated.InstallationID.String(),
		SessionKey:     rated.SessionKey[:],
		Role:           rated.Role,
		RouterUserID:   rated.RouterUserID,
		ClientApp:      rated.Client.TelemetryClientApp(),
		SessionID:      rated.Client.SessionID,
		RequestedModel: rated.RequestedModel,
		ServedModel:    rated.servedModel,
		Rating:         rated.rating,
		SuggestedLabel: rated.Command.SuggestedLabel,
		Feedback:       persistedFeedbackText(rated.rating, rated.feedback),
		Source:         RouterFeedbackSourceUser,
		RequestID:      rated.turn.RequestID,
		RouteID:        rated.turn.RouteID,
	}
	if err := f.Store.InsertRouterFeedback(context.WithoutCancel(ctx), event); err != nil {
		observability.FromContext(ctx).Error("/router-feedback: feedback insert failed", "err", err)
		return err
	}
	return nil
}

func (f RouterFeedback) upsertRequestFeedback(ctx context.Context, rated ratedFeedback) {
	if rated.turn.RequestID == "" || rated.rating == "" || f.Requests == nil {
		return
	}
	err := f.Requests.UpsertRequestFeedback(context.WithoutCancel(ctx), RequestFeedback{
		InstallationID: rated.InstallationID.String(),
		ExternalID:     rated.ExternalID,
		RequestID:      rated.turn.RequestID,
		Rating:         rated.rating,
		Comment:        rated.feedback,
		Source:         "router-feedback-command",
		RouterUserID:   rated.RouterUserID,
	})
	if err != nil {
		observability.FromContext(ctx).Error("/router-feedback: request_feedback upsert failed", "rated_request_id", rated.turn.RequestID, "err", err)
	}
}

func (f RouterFeedback) reportToStrategy(ctx context.Context, rated ratedFeedback) {
	strategy := rated.Strategy
	if rated.turn.Strategy != "" {
		strategy = router.Strategy(rated.turn.Strategy)
	}
	reporter, ok := f.Reporters.Reporter(strategy)
	if !ok {
		return
	}
	owned := f.Reporters.Owns(strategy)
	var trainingDelta []router.ConversationMessage
	if (rated.Command.Sequence == 0 || rated.Command.Sequence == -1) && owned && rated.TrainingAllowed {
		trainingDelta = rated.TrainingDelta()
	}
	payload := map[string]interface{}{
		"strategy":          string(strategy),
		"feedback_key":      hex.EncodeToString(rated.SessionKey[:]),
		"feedback_role":     rated.Role,
		"rating":            rated.rating,
		"feedback":          rated.feedback,
		"requested_model":   rated.RequestedModel,
		"served_model":      rated.servedModel,
		"router_user_id":    rated.RouterUserID,
		"client_app":        rated.Client.TelemetryClientApp(),
		"client_session_id": rated.Client.SessionID,
		"source":            RouterFeedbackSourceUser,
		"request_id":        rated.turn.RequestID,
		"route_id":          rated.turn.RouteID,
	}
	if rated.Command.SuggestedLabel != "" {
		payload["suggested_label"] = rated.Command.SuggestedLabel
	}
	if rated.ExternalID != "" {
		payload["organization_id"] = rated.ExternalID
	}
	payload["rollout_id"] = rated.RolloutID
	payload["training_allowed"] = rated.TrainingAllowed
	if rated.InstallationID != uuid.Nil {
		payload["installation_id"] = rated.InstallationID.String()
	}
	if owned && rated.TrainingAllowed && len(trainingDelta) > 0 {
		payload["training_conversation_delta"] = trainingDelta
	}
	log := observability.FromContext(ctx)
	if identity, managed := requestcontext.ServingIdentityFromContext(ctx); managed {
		payload["serving_identity"] = identity
	}
	observability.SafeGoContext(ctx, log, f.ReportTimeout, "reportPolicyFeedback", func(reportCtx context.Context) {
		if err := reporter.ReportFeedback(reportCtx, payload); err != nil {
			log.Error("/router-feedback: policy feedback report failed", "strategy", strategy, "err", err)
		}
	})
}

func (f RouterFeedback) recordSpan(ctx context.Context, rated ratedFeedback) {
	now := f.Now()
	attrs := telemetry.NewAttrBuilder(15).
		String("external_id", rated.ExternalID).
		String("router_user_id", rated.RouterUserID).
		String("client.device_id", rated.Client.DeviceID).
		String("client.session_id", rated.Client.SessionID).
		String("client.user_agent", rated.Client.UserAgent).
		String("client.app", rated.Client.TelemetryClientApp()).
		String("requested.model", rated.RequestedModel).
		String("feedback.served_model", rated.servedModel).
		String("feedback.role", rated.Role).
		String("feedback.rating", rated.rating).
		Int64("feedback.sequence", int64(rated.Command.Sequence)).
		String("feedback.text", rated.feedback).
		String("feedback.source", RouterFeedbackSourceUser)
	if rated.turn.RequestID != "" {
		attrs = attrs.String("feedback.request_id", rated.turn.RequestID)
	}
	if rated.turn.RouteID != "" {
		attrs = attrs.String("feedback.route_id", rated.turn.RouteID)
	}
	telemetry.Record(ctx, telemetry.Span{
		Name:  routerFeedbackCommandSpanName,
		Start: now,
		End:   now,
		Attrs: attrs.Build(),
	})
	telemetry.Flush(ctx)
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
