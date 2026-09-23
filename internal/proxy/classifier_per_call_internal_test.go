package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

func TestClassifierPerCallHistoryWithinSingleHumanTurn(t *testing.T) {
	var inputs []router.AtomicClassificationRequest
	svc, principal, store := classifierSessionFixture(t, func(_ context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		inputs = append(inputs, input)
		complexity := router.ClassifierComplexity(input.User.Features.ToolCallCount % 4)
		probabilities := make([]float64, 4)
		probabilities[complexity] = 1
		return router.ClassifierPrediction{Complexity: complexity, Probabilities: probabilities}, nil
	})
	ctx := classifierAdmit(t, svc, principal)
	observation := classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "Synthetic task"))
	failed := true
	for call := range 14 {
		input, err := classifierContextForCall(observation)
		require.NoError(t, err)
		prediction, err := svc.classifyThread(ctx, input)
		require.NoError(t, err)
		require.Equal(t, router.ClassifierComplexity(call%4), prediction.Complexity)
		require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: call, ToolErrorCount: call}, inputs[call].User.Features)
		require.Equal(t, call*2, inputs[call].CompletedResponseCount)
		require.Len(t, inputs[call].User.PrecedingResponses, min(10, call*2))
		for _, response := range inputs[call].User.PrecedingResponses {
			require.Equal(t, router.ClassifierComplexity((response.ResponseIndex/2)%4), response.Complexity)
		}
		// A replacement replica must deduplicate this call, not the human turn.
		replica := &Service{now: svc.now}
		require.NoError(t, replica.WithClassifierSessions(svc.classifierSessions.config, store, svc.classifierSessions.classifier))
		retried, err := replica.classifyThread(ctx, input)
		require.NoError(t, err)
		require.Equal(t, prediction, retried)
		require.Len(t, inputs, call+1)
		svc = replica
		callID := fmt.Sprintf("call-%d", call)
		// Multiple output messages and an empty block from ONE API invocation
		// must all retain that invocation's prediction, not invent new calls.
		observation.Messages = append(observation.Messages,
			classifierTestText(translate.EscalationRoleAssistant, fmt.Sprintf("response-%d", call)),
			translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{
				{Type: translate.EscalationBlockText, Text: ""},
				{Type: translate.EscalationBlockToolCall, ID: callID, Name: "test"},
			}},
			translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{
				{Type: translate.EscalationBlockToolResult, CallID: callID, IsError: &failed},
			}},
		)
	}
	require.Equal(t, "response-8", inputs[13].User.PrecedingResponses[0].Content)
	require.Empty(t, inputs[13].User.PrecedingResponses[9].Content)
}

func TestClassifierResponsesToolLoopSwitchesDispatchedModel(t *testing.T) {
	var inputs []router.AtomicClassificationRequest
	fixture, principal, store := classifierSessionFixture(t, classifierMedium)
	config := fixture.classifierSessions.config
	classifierServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/classify", r.URL.Path)
		var input router.AtomicClassificationRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		inputs = append(inputs, input)
		complexity := router.ClassifierLow
		if input.User.Features.ToolErrorCount > 0 {
			complexity = router.ClassifierHigh
		}
		probabilities := make([]float64, 4)
		probabilities[complexity] = 1
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"release": config.Release, "release_sha256": config.ReleaseSHA256,
			"prediction": complexity, "probabilities": probabilities, "input_tokens": 600,
			"history_source": router.ClassifierHistoricalPrediction,
		}))
	}))
	defer classifierServer.Close()
	classifier, err := policyclient.NewAtomicClassifier(classifierServer.URL, strings.Repeat("k", 32), config.Release, config.ReleaseSHA256, classifierServer.Client())
	require.NoError(t, err)
	const smallModel catalog.ModelID = "gpt-5.6-luna"
	const largeModel catalog.ModelID = "gpt-5.6-sol"
	upstream := &classifierResponseProvider{}
	baseline := &betaTestRouter{}
	svc := NewService(baseline, map[string]providers.Client{providers.ProviderOpenAI: upstream}, nil, false, nil, nil, false, providers.ProviderOpenAI, smallModel.String(), nil)
	require.NoError(t, svc.WithClassifierSessions(config, store, classifier))
	roster := &rosterdata.Roster{SchemaVersion: rosterdata.SchemaVersionPolicyV1, SHA256: config.SelectionPolicySHA256,
		ClassOrder: []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}, Clusters: map[string]rosterdata.Cluster{}}
	for index, class := range roster.ClassOrder {
		modelID := smallModel
		if index >= int(router.ClassifierHigh) {
			modelID = largeModel
		}
		model, found := catalog.ByID(modelID.String())
		require.True(t, found)
		arm := armid.ForModel(model)
		roster.Clusters[class] = rosterdata.Cluster{Arms: []string{arm}, ArmScores: map[string]float64{arm: 1}}
	}
	resolver := policy.NewResolver(map[string]struct{}{smallModel.String(): {}, largeModel.String(): {}}, map[string]struct{}{providers.ProviderOpenAI: {}}, armid.ForModel, policy.ManagedProviderPolicy())
	capabilities := policy.Capabilities{SchemaVersion: policy.SchemaVersionV4, AuthoritativePerTurnSelection: true}
	routing := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyLLMClassifier, Unavailable: router.ErrClassifierUnavailable,
		ClassifierArtifactID: config.Release, ClassifierArtifactSHA256: config.ReleaseSHA256, SelectionPolicyReleaseID: config.Release, SelectionPolicySHA256: config.SelectionPolicySHA256},
		policy.AtomicClassifierFacts{Release: config.Release, ReleaseSHA256: config.ReleaseSHA256}, resolver).WithCapabilities(capabilities).WithArmSelector(selection.Selector(roster))
	svc.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyLLMClassifier, Router: routing, Capabilities: capabilities, Unavailable: router.ErrClassifierUnavailable})
	ctx := classifierAdmit(t, svc, principal)
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	const first = `{"model":"auto","stream":true,"input":[{"role":"user","content":"Synthetic task"}]}`
	const continuation = `{"model":"auto","stream":true,"input":[{"role":"user","content":"Synthetic task"},{"role":"assistant","content":[{"type":"output_text","text":"Checking"}]},{"type":"function_call","call_id":"c1","name":"exec_command","arguments":"{\"cmd\":\"exit 7\"}"},{"type":"function_call_output","call_id":"c1","output":"Chunk ID: abc123\nWall time: 0.0001 seconds\nProcess exited with code 7\nOriginal token count: 1\nOutput:\nSynthetic output\n"}]}`
	for index, body := range []string{first, continuation, continuation} {
		recorder := httptest.NewRecorder()
		err := svc.ProxyOpenAIResponses(ctx, []byte(body), recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		require.NoError(t, err)
		expectedModel := largeModel
		if index == 0 {
			expectedModel = smallModel
		}
		require.Equal(t, expectedModel.String(), recorder.Header().Get("X-Router-Model"))
		require.Contains(t, string(upstream.body), expectedModel.String())
	}
	require.Len(t, inputs, 2, "only the exact API retry reuses a prediction")
	require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: 1, ToolErrorCount: 1}, inputs[1].User.Features)
	require.Equal(t, []router.PredictedClassifierResponse{{ResponseIndex: 0, Content: "Checking", Complexity: router.ClassifierLow}}, inputs[1].User.PrecedingResponses)
	require.Zero(t, baseline.calls)
}

func TestClassifierCodexExitErrorsAdvanceWithoutChangingReplay(t *testing.T) {
	var inputs []router.AtomicClassificationRequest
	svc, principal, _ := classifierSessionFixture(t, func(_ context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		inputs = append(inputs, input)
		return classifierMedium(context.Background(), input)
	})
	ctx := classifierAdmit(t, svc, principal)
	items := []json.RawMessage{json.RawMessage(`{"role":"user","content":"Synthetic task"}`)}
	var previousPrefixes []string
	for completed := 0; completed <= 12; completed++ {
		body, err := json.Marshal(map[string]any{"input": items})
		require.NoError(t, err)
		observation, err := translate.ParseResponsesEscalationObservation(body)
		require.NoError(t, err)
		original, err := json.Marshal(observation)
		require.NoError(t, err)
		observation.CodexToolResults = true
		input, err := classifierContextForCall(observation)
		require.NoError(t, err)
		replayed, err := json.Marshal(observation)
		require.NoError(t, err)
		require.Equal(t, original, replayed)
		if completed > 0 {
			require.Equal(t, previousPrefixes, input.PrefixDigests[:len(previousPrefixes)])
			require.Nil(t, observation.Messages[len(observation.Messages)-1].Blocks[0].IsError)
		}
		previousPrefixes = input.PrefixDigests
		_, err = svc.classifyThread(ctx, input)
		require.NoError(t, err)
		require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: completed, ToolErrorCount: completed}, inputs[completed].User.Features)
		require.Equal(t, completed, inputs[completed].CompletedResponseCount)
		require.Len(t, inputs[completed].User.PrecedingResponses, min(10, completed))
		_, err = svc.classifyThread(ctx, input)
		require.NoError(t, err)
		require.Len(t, inputs, completed+1, "exact-prefix retries reuse their prediction")
		items = append(items,
			json.RawMessage(`{"role":"assistant","content":[{"type":"output_text","text":""}]}`),
			json.RawMessage(fmt.Sprintf(`{"type":"function_call","call_id":"exec-%d","name":"exec_command","arguments":"{\"cmd\":\"exit 7\"}"}`, completed)),
			json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":"exec-%d","output":"Chunk ID: abc123\nWall time: 0.0001 seconds\nProcess exited with code 7\nOriginal token count: 1\nOutput:\nSynthetic output\n"}`, completed)),
		)
	}
}

func TestClassifierLegacyPredictionsCannotBecomeCallHistory(t *testing.T) {
	svc, principal, store := classifierSessionFixture(t, classifierMedium)
	ctx := classifierAdmit(t, svc, principal)
	input, err := classifierContextForCall(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "Synthetic task")))
	require.NoError(t, err)
	prediction, err := svc.classifyThread(ctx, input)
	require.NoError(t, err)
	thread := ctx.Value(classifierThreadContextKey{}).(router.ClassifierThread)
	prediction.InputMessageCount = 0
	store.turns[thread.ThreadID][prediction.TurnDigest] = prediction
	_, err = svc.classifyThread(ctx, input)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
}

func TestClassifierMissingUserPredictionCannotInheritOlderCall(t *testing.T) {
	svc, principal, _ := classifierSessionFixture(t, classifierMedium)
	ctx := classifierAdmit(t, svc, principal)
	observation := classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "Synthetic first task"))
	input, err := classifierContextForCall(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, input)
	require.NoError(t, err)
	observation.Messages = append(observation.Messages,
		classifierTestText(translate.EscalationRoleAssistant, "First answer"),
		classifierTestText(translate.EscalationRoleUser, "Unrouted second task"),
		classifierTestText(translate.EscalationRoleAssistant, "Unrouted second answer"),
		classifierTestText(translate.EscalationRoleUser, "Third task"),
	)
	input, err = classifierContextForCall(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, input)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
}
