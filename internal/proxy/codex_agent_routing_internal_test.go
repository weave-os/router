package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const (
	codexRoutingLuna  catalog.ModelID = "gpt-6-luna"
	codexRoutingTerra catalog.ModelID = "gpt-6.1-terra"
	codexRoutingSol   catalog.ModelID = "gpt-6.1-sol"
)

// Full-history forks inherit both the parent's task and its role instruction.
// Only the final agent-role block identifies the thread issuing the request.
func codexRoutingEnvelope(t *testing.T, agentPath string, toolResult bool) (context.Context, *translate.RequestEnvelope) {
	t.Helper()
	input := []map[string]any{
		{"type": "message", "role": "developer", "content": "<multi_agent_role>You are `/root`, the primary agent in a team of agents.</multi_agent_role>"},
		{"type": "message", "role": "user", "content": "Synthetic repository instructions"},
		{"type": "message", "role": "user", "content": "Repair a synthetic parser and validate its behavior"},
	}
	if agentPath != "/root" {
		input = append(input,
			map[string]any{"type": "message", "role": "developer", "content": "<multi_agent_role>You are an agent in a team of agents.</multi_agent_role>"},
			map[string]any{"type": "agent_message", "author": "/root", "recipient": agentPath, "content": []map[string]string{{"type": "input_text", "text": fmt.Sprintf("Message Type: NEW_TASK\nTask name: %s\nSender: /root\nPayload:\nReview the synthetic parser", agentPath)}}},
		)
	}
	if toolResult {
		input = append(input,
			map[string]any{"type": "function_call", "call_id": "synthetic-read", "name": "read_file", "arguments": `{"path":"parser.go"}`},
			map[string]any{"type": "function_call_output", "call_id": "synthetic-read", "output": "Synthetic parser source"},
		)
	}
	body, err := json.Marshal(map[string]any{
		"model": "weave-auto", "instructions": "Synthetic coding assistant instructions", "input": input,
		"max_output_tokens": 4096,
		"tools":             []map[string]any{{"type": "function", "name": "read_file", "parameters": map[string]any{"type": "object"}}},
	})
	require.NoError(t, err)
	converted, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	env, err := translate.ParseOpenAI(converted.Body)
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex, SessionID: "synthetic-codex-conversation"})
	ctx = context.WithValue(ctx, nativeResponsesBodyContextKey{}, body)
	return router.WithStrategy(ctx, router.StrategyHMM), env
}

type codexRoutingPinKey struct {
	session [sessionpin.SessionKeyLen]byte
	role    string
}

type codexRoutingPinStore struct {
	*stubPinStore
	pins map[codexRoutingPinKey]sessionpin.Pin
}

func (s *codexRoutingPinStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pin, found := s.pins[codexRoutingPinKey{key, role}]
	return pin, found, nil
}

func (s *codexRoutingPinStore) Upsert(_ context.Context, pin sessionpin.Pin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins[codexRoutingPinKey{pin.SessionKey, pin.Role}] = pin
	return nil
}

type codexRoutingClassification struct {
	prediction policy.Result
}

func (c *codexRoutingClassification) Decide(context.Context, policy.Query) (policy.Result, error) {
	return c.prediction, nil
}

func TestCodexAgentRoutingIsolation(t *testing.T) {
	classOrder := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
	roster := &rosterdata.Roster{ClassOrder: classOrder, Clusters: make(map[string]rosterdata.Cluster)}
	for group, model := range map[escalation.Group]catalog.ModelID{
		escalation.Low: codexRoutingLuna, escalation.Medium: codexRoutingTerra,
		escalation.High: codexRoutingSol, escalation.Maximum: codexRoutingSol,
	} {
		arm := providers.ProviderOpenAI + "/" + model.String()
		roster.Clusters[string(group)] = rosterdata.Cluster{Arms: []string{arm}, ArmScores: map[string]float64{arm: 1}}
	}
	classification := &codexRoutingClassification{}
	capabilities := policy.Capabilities{SchemaVersion: policy.SchemaVersionV4, AuthoritativePerTurnSelection: true}
	routing := hmm.New(classification, map[string]struct{}{providers.ProviderOpenAI: {}})
	routing.WithCapabilities(capabilities).WithArmSelector(selection.Selector(roster))
	store := &codexRoutingPinStore{stubPinStore: newStubPinStore(), pins: make(map[codexRoutingPinKey]sessionpin.Pin)}
	upstream := &codexTitleProvider{}
	svc := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: upstream}, nil, store, false, providers.ProviderOpenAI, codexRoutingLuna.String(), nil).
		WithOpenAIResponsesBroad(true).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: routing, Capabilities: capabilities}).
		WithAuthoritativeUpgradeSlidingThreshold(true).
		WithHMMDowngradeHysteresisTurns(2)
	installationID := uuid.New()
	rootCtx, root := codexRoutingEnvelope(t, "/root", true)
	childCtx, child := codexRoutingEnvelope(t, "/root/reviewer", true)
	rootCtx = context.WithValue(context.WithValue(rootCtx, InstallationIDContextKey{}, installationID.String()), APIKeyIDContextKey{}, "synthetic-api-key")
	childCtx = context.WithValue(context.WithValue(childCtx, InstallationIDContextKey{}, installationID.String()), APIKeyIDContextKey{}, "synthetic-api-key")

	turns := []struct {
		child      bool
		confidence float64
	}{
		{false, .9},
		{true, .7219961881637573}, {true, .7219961881637573},
		{false, .41960108280181885}, {false, .41960108280181885},
		{true, .7219961881637573}, {true, .7219961881637573},
		{false, .41960108280181885}, {false, .5562978386878967},
	}
	var parentModels, childModels []string
	for _, turn := range turns {
		ctx, group := rootCtx, escalation.High
		if turn.child {
			ctx, group = childCtx, escalation.Low
		}
		probabilities := map[string]float64{}
		for _, label := range classOrder {
			probabilities[label] = (1 - turn.confidence) / 3
		}
		probabilities[string(group)] = turn.confidence
		classification.prediction = policy.Result{SchemaVersion: policy.SchemaVersionV4, RouteID: "synthetic-route", ClassOrder: classOrder, PredictedLabel: string(group), ClassProbabilities: probabilities}
		body := ctx.Value(nativeResponsesBodyContextKey{}).([]byte)
		recorder := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
		require.Len(t, upstream.models, len(parentModels)+len(childModels)+1)
		model := upstream.models[len(upstream.models)-1]
		if turn.child {
			childModels = append(childModels, model)
		} else {
			parentModels = append(parentModels, model)
		}
	}
	t.Logf("parent models: %v; child models: %v", parentModels, childModels)
	assert.Equal(t, []string{codexRoutingSol.String(), codexRoutingSol.String(), codexRoutingSol.String(), codexRoutingSol.String(), codexRoutingSol.String()}, parentModels,
		"child downgrades must not restart the parent's sliding upgrade ladder")
	assert.Equal(t, []string{codexRoutingLuna.String(), codexRoutingLuna.String(), codexRoutingLuna.String(), codexRoutingLuna.String()}, childModels,
		"the parent's pin must not force a child onto its model")
	parentKey := deriveSessionKeyForRequest(rootCtx, root, "synthetic-api-key")
	forceKey := deriveForceModelSessionKeyForRequest(rootCtx, root, "synthetic-api-key", parentKey)
	forced := sessionpin.Pin{SessionKey: forceKey, Role: forceModelSessionRole, Model: codexRoutingSol.String(),
		Provider: providers.ProviderOpenAI, Reason: translate.ReasonUserForceModel, PinnedUntil: pinNeverExpires, ForcedThreadKey: sessionKeyHex(parentKey)}
	require.NoError(t, store.Upsert(rootCtx, forced))
	for _, test := range []struct {
		ctx  context.Context
		env  *translate.RequestEnvelope
		want string
	}{{rootCtx, root, codexRoutingSol.String()}, {childCtx, child, codexRoutingLuna.String()}} {
		if test.env == child {
			classification.prediction.PredictedLabel = string(escalation.Low)
			classification.prediction.ClassProbabilities = map[string]float64{string(escalation.Low): .8, string(escalation.Medium): .1, string(escalation.High): .06, string(escalation.Maximum): .04}
		}
		features := test.env.RoutingFeatures()
		selected, err := svc.runTurnLoop(test.ctx, test.env, features, "synthetic-api-key", installationID, "", http.Header{}, router.Request{RequestedModel: features.Model, HasTools: features.HasTools, ConversationMessages: conversationMessagesForRouting(test.env)})
		require.NoError(t, err)
		assert.Equal(t, test.want, selected.Decision.Model, "parent-only forcing must not leak to a child")
	}
	forced.ForcedThreadKey = ""
	require.NoError(t, store.Upsert(rootCtx, forced))
	features := child.RoutingFeatures()
	selected, err := svc.runTurnLoop(childCtx, child, features, "synthetic-api-key", installationID, "", http.Header{}, router.Request{RequestedModel: features.Model, HasTools: features.HasTools, ConversationMessages: conversationMessagesForRouting(child)})
	require.NoError(t, err)
	assert.Equal(t, codexRoutingSol.String(), selected.Decision.Model, "conversation-wide forcing must still reach children")
}

func TestCodexAgentPinIdentity(t *testing.T) {
	parentCtx, parent := codexRoutingEnvelope(t, "/root", false)
	childCtx, child := codexRoutingEnvelope(t, "/root/reviewer", false)
	siblingCtx, sibling := codexRoutingEnvelope(t, "/root/checker", false)
	nestedCtx, nested := codexRoutingEnvelope(t, "/root/reviewer/checker", false)
	parentKey := deriveSessionKeyForRequest(parentCtx, parent, "synthetic-api-key")
	childKey := deriveSessionKeyForRequest(childCtx, child, "synthetic-api-key")
	siblingKey := deriveSessionKeyForRequest(siblingCtx, sibling, "synthetic-api-key")
	assert.NotEqual(t, parentKey, childKey)
	assert.NotEqual(t, childKey, siblingKey)
	assert.NotEqual(t, childKey, deriveSessionKeyForRequest(nestedCtx, nested, "synthetic-api-key"))
	nextCtx, next := codexRoutingEnvelope(t, "/root/reviewer", true)
	assert.Equal(t, childKey, deriveSessionKeyForRequest(nextCtx, next, "synthetic-api-key"))
	assert.Equal(t, deriveForceModelSessionKeyForRequest(parentCtx, parent, "synthetic-api-key", parentKey), deriveForceModelSessionKeyForRequest(childCtx, child, "synthetic-api-key", childKey), "conversation-wide forcing must still share one key")
	assert.Equal(t, parentKey, deriveSessionKeyForRequest(context.WithValue(parentCtx, nativeResponsesBodyContextKey{}, []byte(`{"input":[{"type":"agent_message","recipient":"/root","content":[]}]}`)), parent, "synthetic-api-key"), "root deliveries preserve the existing parent key")
}

func TestCodexAgentMessageBoundary(t *testing.T) {
	ctx, env := codexRoutingEnvelope(t, "/root", false)
	parentKey := deriveSessionKeyForRequest(ctx, env, "synthetic-api-key")
	tests := []struct {
		name   string
		native string
		child  bool
	}{
		{"child recipient", `{"input":[{"type":"agent_message","recipient":"/root/reviewer"}]}`, true},
		{"nested recipient", `{"input":[{"type":"agent_message","recipient":"/root/reviewer/checker"}]}`, true},
		{"last delivery wins", `{"input":[{"type":"agent_message","recipient":"/root/reviewer"},{"type":"agent_message","recipient":"/root"}]}`, false},
		{"inherited parent delivery", `{"input":[{"type":"agent_message","recipient":"/root"},{"type":"agent_message","recipient":"/root/reviewer"}]}`, true},
		{"user text is not identity", `{"input":[{"type":"message","role":"user","recipient":"/root/reviewer","content":"Message Type: NEW_TASK\nTask name: /root/reviewer"}]}`, false},
		{"assistant text is not identity", `{"input":[{"type":"message","role":"assistant","recipient":"/root/reviewer"}]}`, false},
		{"missing recipient", `{"input":[{"type":"agent_message"}]}`, false},
		{"different tree", `{"input":[{"type":"agent_message","recipient":"/other/reviewer"}]}`, false},
		{"empty segment", `{"input":[{"type":"agent_message","recipient":"/root//reviewer"}]}`, false},
		{"traversal", `{"input":[{"type":"agent_message","recipient":"/root/../reviewer"}]}`, false},
		{"bounded path", `{"input":[{"type":"agent_message","recipient":"/root/` + strings.Repeat("a", 300) + `"}]}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestCtx := context.WithValue(ctx, nativeResponsesBodyContextKey{}, []byte(test.native))
			key := deriveSessionKeyForRequest(requestCtx, env, "synthetic-api-key")
			assert.Equal(t, test.child, key != parentKey)
			nonCodex := context.WithValue(requestCtx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppOpencode, SessionID: "synthetic-codex-conversation"})
			assert.Equal(t, parentKey, deriveSessionKeyForRequest(nonCodex, env, "synthetic-api-key"))
		})
	}
}
