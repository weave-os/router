package decisionmodel

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

const EscalationCadence = 3

const maxTrackedSessions = 10_000

const clientAppCodex = "codex"

type Lineup struct {
	Top                  string
	Fast                 string
	LowTaskJudgment      string
	SubscriptionProvider string
}

var (
	ClaudeCodeLineup = Lineup{Top: string(catalog.ModelIDClaudeOpus48), Fast: string(catalog.ModelIDClaudeHaiku45), LowTaskJudgment: "z-ai/glm-5.3", SubscriptionProvider: providers.ProviderAnthropic}
	CodexLineup      = Lineup{Top: "gpt-6.1-sol", Fast: string(catalog.ModelIDGPT56Luna), LowTaskJudgment: "z-ai/glm-5.3", SubscriptionProvider: providers.ProviderOpenAI}
)

type sessionState struct {
	taskComplexity      Choice
	escalated           bool
	fastSinceCheckpoint bool
}

type Router struct {
	classifier Classifier
	mu         sync.Mutex
	sessions   map[string]sessionState
	order      []string
}

func New(classifier Classifier) *Router {
	return &Router{classifier: classifier, sessions: map[string]sessionState{}}
}

func (r *Router) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	lineup := ClaudeCodeLineup
	if strings.EqualFold(req.ClientApp, clientAppCodex) {
		lineup = CodexLineup
	}
	sessionID := req.FeedbackKey
	if sessionID == "" {
		sessionID = req.ClientSessionID
	}
	session := r.load(sessionID)
	_, subscribed := req.SubscriptionProviders[lineup.SubscriptionProvider]
	if subscribed {
		session.taskComplexity = ChoiceHigh
	}
	callIndex := AgentCallIndex(req.ConversationMessages)
	checkpoint := callIndex > 1 && (callIndex-1)%EscalationCadence == 0
	questions := []QuestionID{}
	if !session.escalated {
		questions = append(questions, QuestionCallComplexity)
	}
	if session.taskComplexity == "" {
		questions = append(questions, QuestionTaskComplexity)
	}
	if checkpoint && !session.escalated {
		questions = append(questions, QuestionEscalate)
	}
	answers := map[QuestionID]Answer{}
	if len(questions) > 0 {
		classified, err := r.classifier.Classify(ctx, BuildState(req.ConversationMessages), questions)
		if err != nil {
			return router.Decision{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		answers = classified
	}
	if answer, found := answers[QuestionTaskComplexity]; found {
		session.taskComplexity = answer.Choice
	}
	escalationEligible := session.taskComplexity == ChoiceLow || session.fastSinceCheckpoint
	if answer, found := answers[QuestionEscalate]; found && escalationEligible && answer.Choice == ChoiceYes {
		session.escalated = true
	}
	if checkpoint {
		session.fastSinceCheckpoint = false
	}
	callComplexity := answers[QuestionCallComplexity].Choice
	decision, err := resolve(req, pickModel(lineup, session, callComplexity), lineup)
	if err != nil {
		return router.Decision{}, err
	}
	if decision.Model == lineup.Fast {
		session.fastSinceCheckpoint = true
	}
	r.store(sessionID, session)
	decision.Reason = fmt.Sprintf("decision_model task=%s call=%s escalated=%t subscription=%t call_index=%d", session.taskComplexity, callComplexity, session.escalated, subscribed, callIndex)
	return decision, nil
}

func pickModel(lineup Lineup, session sessionState, callComplexity Choice) string {
	switch {
	case session.escalated:
		return lineup.Top
	case callComplexity == ChoiceLow:
		return lineup.Fast
	case session.taskComplexity == ChoiceHigh:
		return lineup.Top
	default:
		return lineup.LowTaskJudgment
	}
}

func resolve(req router.Request, model string, lineup Lineup) (router.Decision, error) {
	for _, honorAutomaticExclusions := range []bool{true, false} {
		for _, candidate := range []string{model, lineup.Top, lineup.Fast, lineup.LowTaskJudgment} {
			if _, excluded := req.Candidates.AutomaticExcludedModels[candidate]; honorAutomaticExclusions && excluded {
				continue
			}
			if provider, ok := dispatchProvider(req, candidate); ok {
				return router.Decision{Provider: provider, Model: candidate}, nil
			}
		}
	}
	return router.Decision{}, fmt.Errorf("%w: no lineup model is dispatchable", ErrUnavailable)
}

func dispatchProvider(req router.Request, model string) (string, bool) {
	for _, excluded := range []map[string]struct{}{req.Candidates.ExcludedModels, req.Candidates.SafetyExcludedModels, req.Candidates.ContextWindowExcludedModels, req.Candidates.UnsignedHistoryExcludedModels} {
		if _, found := excluded[model]; found {
			return "", false
		}
	}
	if _, allowed := req.Candidates.AllowedModels[model]; len(req.Candidates.AllowedModels) > 0 && !allowed {
		return "", false
	}
	if _, textOnly := catalog.ImageUnsupportedSet()[model]; req.HasImages && textOnly {
		return "", false
	}
	bindings := catalog.EnumerateBindingsWithCustom(model, availableProviders(req, model), req.CustomBindings)
	if len(bindings) == 0 {
		return "", false
	}
	return bindings[0].Provider, true
}

func availableProviders(req router.Request, model string) map[string]struct{} {
	if len(req.GatewayProviders) > 0 {
		gateways := map[string]struct{}{}
		for _, provider := range req.CustomBindings[model] {
			if _, gateway := req.GatewayProviders[provider]; gateway {
				gateways[provider] = struct{}{}
			}
		}
		return gateways
	}
	if req.EnabledProviders != nil {
		return req.EnabledProviders
	}
	entry, _ := catalog.ByID(model)
	allProviders := map[string]struct{}{}
	for _, binding := range entry.Providers {
		allProviders[binding.Provider] = struct{}{}
	}
	return allProviders
}

func (r *Router) load(sessionID string) sessionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[sessionID]
}

func (r *Router) store(sessionID string, session sessionState) {
	if sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	saved, known := r.sessions[sessionID]
	if !known {
		r.order = append(r.order, sessionID)
	}
	session.escalated = session.escalated || saved.escalated
	if saved.taskComplexity != "" {
		session.taskComplexity = saved.taskComplexity
	}
	r.sessions[sessionID] = session
	for len(r.order) > maxTrackedSessions {
		delete(r.sessions, r.order[0])
		r.order = r.order[1:]
	}
}
