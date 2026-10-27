package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type turnStrikePinStore struct {
	rolePinStore
	mu      sync.Mutex
	strikes map[string][]string
	cleared []string
}

func (s *turnStrikePinStore) StrikeTurnModel(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.strikes == nil {
		s.strikes = map[string][]string{}
	}
	s.strikes[role] = append(s.strikes[role], model)
	return nil
}

func (s *turnStrikePinStore) ClearTurnStrikes(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleared = append(s.cleared, role)
	return nil
}

var _ sessionpin.TurnStrikeStore = (*turnStrikePinStore)(nil)

const toolResultTurnBody = `{"model":"claude-opus-4-8","messages":[` +
	`{"role":"user","content":"fix the build"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`

func runTurnLoopWithBody(t *testing.T, svc *Service, body string) turnLoopResult {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	res, err := svc.runTurnLoop(statefulTestContext(), env, features, "api-key", uuid.New(), "", http.Header{}, router.Request{
		RequestedModel:       features.Model,
		EstimatedInputTokens: features.Tokens,
		HasTools:             features.HasTools,
		ConversationMessages: conversationMessagesForRouting(env),
	})
	require.NoError(t, err)
	return res
}

func turnStrikePin() sessionpin.Pin {
	pin := demotedPin(freshTurnModel)
	pin.TurnStrikes = []string{demotedPinModel}
	return pin
}

// A model that failed and was rescued stays out for every tool-result request
// of the same agent turn.
func TestTurnLoopKeepsTurnStrikeOutUntilUserPrompt(t *testing.T) {
	store := &turnStrikePinStore{rolePinStore: rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: turnStrikePin(),
	}}}
	svc, scorer := cooldownTurnLoopService(store, false)

	res := runTurnLoopWithBody(t, svc, toolResultTurnBody)

	require.NotEmpty(t, scorer.requests)
	for _, req := range scorer.requests {
		assert.Contains(t, req.AutomaticExcludedModels, demotedPinModel)
	}
	assert.Contains(t, res.SessionDemotedModels, demotedPinModel)
	assert.Empty(t, store.cleared)
}

// The next real user prompt starts a new agent turn: the struck model is
// eligible again and the stored strikes are cleared.
func TestTurnLoopUserPromptClearsTurnStrikes(t *testing.T) {
	store := &turnStrikePinStore{rolePinStore: rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: turnStrikePin(),
	}}}
	svc, scorer := cooldownTurnLoopService(store, false)

	res := runDemotionTurnLoop(t, svc, statefulTestContext())

	for _, req := range scorer.requests {
		assert.NotContains(t, req.AutomaticExcludedModels, demotedPinModel)
	}
	assert.NotContains(t, res.SessionDemotedModels, demotedPinModel)
	assert.ElementsMatch(t, []string{sessionpin.DefaultRole, hmmHistoryRole(sessionpin.DefaultRole)}, store.cleared)
}

// With session-lifetime demotion off, a rescued primary failure is still kept
// out for the agent turn on every row the next request merges.
func TestRescuedFailureStrikesModelForAgentTurn(t *testing.T) {
	store := &turnStrikePinStore{}
	svc := newRescuedDemotionTestService(store, false)
	ctx := withTurnFailures(statefulTestContext())

	demoted, _ := strikeRescuedPrimary(svc, ctx, &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Headers: http.Header{}})
	assert.Empty(t, demoted, "no session-lifetime demotion while the flag is off")

	served := router.Decision{Model: freshTurnModel, Reason: "cluster:v0.2"}
	svc.strikeRescuedModelsForTurn(ctx, nil, served, false, nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
	assert.Equal(t, map[string][]string{
		sessionpin.DefaultRole:                 {demotedArm},
		hmmHistoryRole(sessionpin.DefaultRole): {demotedArm},
	}, store.strikes)
}

func TestTurnStrikeSkippedWhenTurnFailedOrPinned(t *testing.T) {
	served := router.Decision{Model: freshTurnModel, Reason: "cluster:v0.2"}
	for name, strike := range map[string]func(*Service, context.Context){
		"turn failed": func(svc *Service, ctx context.Context) {
			svc.strikeRescuedModelsForTurn(ctx, assert.AnError, served, false, nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
		},
		"hard pinned": func(svc *Service, ctx context.Context) {
			svc.strikeRescuedModelsForTurn(ctx, nil, served, true, nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
		},
		"forced model": func(svc *Service, ctx context.Context) {
			forced := router.Decision{Model: freshTurnModel, Reason: translate.ReasonUserForceModel}
			svc.strikeRescuedModelsForTurn(ctx, nil, forced, false, nonZeroSessionKey(), sessionpin.DefaultRole, sessionpin.DefaultRole)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &turnStrikePinStore{}
			svc := newRescuedDemotionTestService(store, false)
			ctx := withTurnFailures(statefulTestContext())
			recordTurnFailure(ctx, demotedArm)
			strike(svc, ctx)
			assert.Empty(t, store.strikes)
		})
	}
}

const siblingAnthropicStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"sibling\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"rescued\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// siblingChainClient fails the listed models and serves every other model.
type siblingChainClient struct {
	failing map[string]bool
	failed  []string
}

func (c *siblingChainClient) Proxy(_ context.Context, decision router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	if c.failing[decision.Model] {
		c.failed = append(c.failed, decision.Model)
		return &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Body: []byte(`{"type":"error","error":{"type":"overloaded_error"}}`)}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, siblingAnthropicStream)
	return nil
}

func (c *siblingChainClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

// Every sibling that fails before a later sibling serves is struck for the
// turn, not only the primary.
func TestSiblingRescueRecordsEveryFailedModel(t *testing.T) {
	anthropicClient := &siblingChainClient{failing: map[string]bool{"claude-opus-5": true}}
	openAIClient := &siblingChainClient{failing: map[string]bool{"gpt-5.6-luna": true}}
	decision := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-opus-5",
		Reason:   "test",
		Metadata: &router.RoutingMetadata{
			CandidateModels:    []string{"claude-opus-5", "gpt-5.6-luna", "claude-sonnet-5-5"},
			CandidateProviders: map[string]string{"gpt-5.6-luna": providers.ProviderOpenAI, "claude-sonnet-5-5": providers.ProviderAnthropic},
		},
	}
	svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{
		providers.ProviderAnthropic: anthropicClient,
		providers.ProviderOpenAI:    openAIClient,
	}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}})
	svc.retrySleep = noopSleep
	ctx := withTurnFailures(statefulTestContext())

	body := `{"model":"claude-opus-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"read main.go"}]}`
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))))

	require.Contains(t, rec.Body.String(), "rescued")
	require.Contains(t, anthropicClient.failed, "claude-opus-5")
	require.Contains(t, openAIClient.failed, "gpt-5.6-luna", "the first sibling fails before the second serves")
	assert.ElementsMatch(t, []string{"claude-opus-5", "gpt-5.6-luna"}, turnFailuresFromContext(ctx).models)
}
