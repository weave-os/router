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

// EscalationCadence is the number of calls between escalation checks, which begin at call 4.
const EscalationCadence = 3

const maxTrackedSessions = 10_000

const clientAppCodex = "codex"

// Lineup is one harness's models: Top for high-task judgment and escalation,
// Fast for defined work, LowTaskJudgment for judgment calls on low tasks.
// SubscriptionProvider is the provider whose subscription skips task judgment.
type Lineup struct {
	Top                  string
	Fast                 string
	LowTaskJudgment      string
	SubscriptionProvider string
}

var (
	// ClaudeCodeLineup serves Claude Code and every non-Codex client.
	ClaudeCodeLineup = Lineup{Top: string(catalog.ModelIDClaudeOpus48), Fast: string(catalog.ModelIDClaudeHaiku45), LowTaskJudgment: "z-ai/glm-5.3", SubscriptionProvider: providers.ProviderAnthropic}
	// CodexLineup is used only for Codex; its subscription provider is OpenAI.
	CodexLineup = Lineup{Top: "gpt-6.1-sol", Fast: string(catalog.ModelIDGPT56Luna), LowTaskJudgment: "z-ai/glm-5.3", SubscriptionProvider: providers.ProviderOpenAI}
)

// sessionState is what the router remembers per session between calls.
type sessionState struct {
	taskComplexity      Choice
	escalated           bool
	fastSinceCheckpoint bool
}

// Router asks the decision model for task complexity, call complexity and periodic escalation,
// then picks the model in Go.
type Router struct {
	classifier Classifier
	mu         sync.Mutex
	sessions   map[string]sessionState
	order      []string
}

// New returns a decision model router that keeps per-session state in memory, evicting the
// oldest session past maxTrackedSessions.
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
	// Escalation is checked before calls 4, 7, 10, ...
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

// resolve keeps the picked model when the request can dispatch it and otherwise
// falls back through the lineup from strongest to cheapest. Automatic
// exclusions are dropped only when they leave no lineup model.
func resolve(req router.Request, model string, lineup Lineup) (router.Decision, error) {
	for _, honorAutomaticExclusions := range []bool{true, false} {
		for _, candidate := range []string{model, lineup.Top, lineup.Fast, lineup.LowTaskJudgment} {
			if _, excluded := req.AutomaticExcludedModels[candidate]; honorAutomaticExclusions && excluded {
				continue
			}
			if provider, ok := dispatchProvider(req, candidate); ok {
				return router.Decision{Provider: provider, Model: candidate}, nil
			}
		}
	}
	return router.Decision{}, fmt.Errorf("%w: no lineup model is dispatchable", ErrUnavailable)
}

// dispatchProvider returns the first provider that can serve model for req,
// honoring exclusions, allowlists, image support, custom bindings and gateways.
func dispatchProvider(req router.Request, model string) (string, bool) {
	for _, excluded := range []map[string]struct{}{req.ExcludedModels, req.SafetyExcludedModels, req.ContextWindowExcludedModels, req.UnsignedHistoryExcludedModels} {
		if _, found := excluded[model]; found {
			return "", false
		}
	}
	if _, allowed := req.AllowedModels[model]; len(req.AllowedModels) > 0 && !allowed {
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

// availableProviders limits gateway installations to their aliased gateways
// and treats a nil EnabledProviders as every catalog provider.
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

// store merges session into the saved state so an overlapping call that
// loaded older state cannot undo an escalation or reclassify the task.
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
