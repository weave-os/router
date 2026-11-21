package proxy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/conversation"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

func (s *Service) WithRouterFeedbackStore(store feedback.RouterFeedbackStore) *Service {
	if s.lifecycle == nil {
		s.lifecycle = &conversation.Lifecycle{}
	}
	s.lifecycle.SetRouterFeedbackStore(store)
	return s
}

type routerFeedbackPorts struct {
	s *Service
}

func (p routerFeedbackPorts) TurnBySequence(ctx context.Context, installationID uuid.UUID, sessionKey []byte, role string, sequence int) (conversation.FeedbackTurn, bool, error) {
	turn, err := p.s.telemetry.GetTelemetryBySessionSequence(ctx, installationID, sessionKey, role, sequence)
	if err != nil {
		return conversation.FeedbackTurn{}, errors.Is(err, sql.ErrNoRows), err
	}
	return conversation.FeedbackTurn{DecisionModel: turn.DecisionModel, RequestID: turn.RequestID, RouteID: turn.RouteID, Strategy: turn.Strategy}, false, nil
}

func (p routerFeedbackPorts) ServedModel(ctx context.Context, strategy router.Strategy, sessionKey [sessionpin.SessionKeyLen]byte, role string) string {
	pin := sessionpin.Pin{}
	if storedPin, found, err := p.s.pinStore.Get(ctx, sessionKey, role); err != nil {
		observability.FromContext(ctx).Error("/router-feedback: pin lookup failed", "err", err)
	} else if found && pinMatchesEffectiveStrategy(strategy, storedPin) {
		pin = storedPin
	}
	forceHistory := p.s.loadForceModelHistory(ctx, sessionKey, role)
	servedModel, _ := switchHistoryFromPins(pin, forceHistory)
	if servedModel == "" {
		servedModel = pin.Model
	}
	return servedModel
}

func (p routerFeedbackPorts) UpsertRequestFeedback(ctx context.Context, request conversation.RequestFeedback) error {
	return p.s.feedbackRepo.Upsert(ctx, feedback.UpsertFeedbackParams{
		InstallationID: request.InstallationID,
		ExternalID:     request.ExternalID,
		RequestID:      request.RequestID,
		Rating:         request.Rating,
		Comment:        &request.Comment,
		Source:         request.Source,
		RouterUserID:   request.RouterUserID,
	})
}

func (p routerFeedbackPorts) Reporter(strategy router.Strategy) (policy.FeedbackReporter, bool) {
	registered, ok := p.s.strategies[strategy]
	return registered.feedback, ok && registered.feedback != nil
}

func (p routerFeedbackPorts) Owns(strategy router.Strategy) bool {
	return p.s.strategyPorts.Ownership.Owns(strategy)
}

func newRouterFeedback(s *Service) conversation.RouterFeedback {
	ports := routerFeedbackPorts{s: s}
	recorder := conversation.RouterFeedback{
		Store:         s.lifecycle.RouterFeedbackStore(),
		Reporters:     ports,
		Now:           s.clock.Now,
		ReportTimeout: policyFeedbackReportTimeout,
	}
	if s.telemetry != nil {
		recorder.Turns = ports
	}
	if s.pinStore != nil {
		recorder.ServedModels = ports
	}
	if s.feedbackRepo != nil {
		recorder.Requests = ports
	}
	return recorder
}

type routerFeedbackCommand struct {
	w           http.ResponseWriter
	env         *translate.RequestEnvelope
	cmd         translate.RouterFeedbackResult
	target      sessionTarget
	inputTokens int
	synthetic   bool
}

func (c routerFeedbackCommand) handle(ctx context.Context, s *Service) error {
	clientID := ClientIdentityFrom(ctx)
	ack := newCommandAcknowledgment(translate.CommandRouterFeedback)
	ack.clientApp = clientID.ClientApp
	externalID, _ := ctx.Value(ExternalIDContextKey{}).(string)
	rolloutID := clientID.RolloutID
	if persistedRolloutID, ok := ctx.Value(PolicyRolloutIDContextKey{}).(string); ok && persistedRolloutID != "" {
		rolloutID = persistedRolloutID
	}
	result, err := newRouterFeedback(s).Record(ctx, conversation.RouterFeedbackRequest{
		Command:         c.cmd,
		InstallationID:  c.target.installationID,
		SessionKey:      c.target.sessionKey,
		Strategy:        c.target.strategy,
		Role:            roleForTier(catalog.TierFor(c.env.Model())),
		RequestedModel:  c.env.Model(),
		Client:          clientID,
		RouterUserID:    auth.UserIDFrom(ctx),
		ExternalID:      externalID,
		RolloutID:       rolloutID,
		TrainingAllowed: policyTrainingAllowedForRequest(ctx),
		TrainingDelta:   func() []router.ConversationMessage { return routerFeedbackTrainingDelta(c.env) },
	})
	if err != nil {
		return err
	}
	ack.feedback = feedbackAcknowledgment(result)
	if result == conversation.RouterFeedbackRecorded {
		ack.rating = c.cmd.Rating
	} else {
		ack.outcome.Disposition = translate.CommandRejected
	}
	if c.synthetic {
		return writeCommandAcknowledgment(ctx, c.w, c.env, ack, c.inputTokens)
	}
	return nil
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
