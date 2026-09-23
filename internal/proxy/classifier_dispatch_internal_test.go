package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type classifierResponseProvider struct{ betaCaptureProvider }

func (p *classifierResponseProvider) Proxy(_ context.Context, decision router.Decision, request providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	p.body = append([]byte(nil), request.Body...)
	if decision.Provider == providers.ProviderOpenAI {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":3}}}\n\n")
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	if decision.Provider == providers.ProviderAnthropic {
		_, err := io.WriteString(w, `{"type":"message","role":"assistant","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":3}}`)
		return err
	}
	_, err := io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3}}`)
	return err
}

func TestClassifierDispatchAcrossProtocols(t *testing.T) {
	for _, test := range []struct {
		name, provider, model, path, firstBody, nextBody string
		toolActivity, thirdUser                          string
		proxyRequest                                     func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", providers.ProviderAnthropic, "claude-sonnet-4-6", "/v1/messages",
			`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"first"}]}`,
			`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"thinking","thinking":"not a response"},{"type":"text","text":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`,
			`{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"test","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","is_error":true,"content":"failed"}]}`,
			`{"role":"user","content":"third"}`,
			(*Service).ProxyMessages},
		{"chat", providers.ProviderOpenAI, catalog.ModelIDGPT55.String(), "/v1/chat/completions",
			fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"first"}]}`, catalog.ModelIDGPT55.String()),
			fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"text","text":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`, catalog.ModelIDGPT55.String()),
			`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"test","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","is_error":true,"content":"failed"}`,
			`{"role":"user","content":"third"}`,
			(*Service).ProxyOpenAIChatCompletion},
		{"responses", providers.ProviderOpenAI, catalog.ModelIDGPT55.String(), "/v1/responses",
			fmt.Sprintf(`{"model":%q,"stream":true,"input":[{"role":"user","content":"first"}]}`, catalog.ModelIDGPT55.String()),
			fmt.Sprintf(`{"model":%q,"stream":true,"input":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"output_text","text":""},{"type":"output_text","text":"answer"}]},{"role":"user","content":"next"}]}`, catalog.ModelIDGPT55.String()),
			`{"type":"function_call","call_id":"c1","name":"test","arguments":"{}"},{"type":"function_call_output","call_id":"c1","status":"failed","output":"failed"}`,
			`{"role":"user","content":"third"}`,
			(*Service).ProxyOpenAIResponses},
		{"gemini", providers.ProviderGoogle, "gemini-2.5-pro", "/v1beta/models/gemini-2.5-pro:generateContent",
			`{"model":"gemini-2.5-pro","contents":[{"role":"user","parts":[{"text":"first"}]}]}`,
			`{"model":"gemini-2.5-pro","contents":[{"role":"user","parts":[{"text":"first"}]},{"role":"model","parts":[{"text":""},{"text":"answer"}]},{"role":"user","parts":[{"text":"next"}]}]}`,
			`{"role":"model","parts":[{"functionCall":{"id":"c1","name":"test","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"test","response":{"error":"failed"}}}]}`,
			`{"role":"user","parts":[{"text":"third"}]}`,
			(*Service).ProxyGeminiGenerateContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			var inputs []router.AtomicClassificationRequest
			fixtureService, principal, store := classifierSessionFixture(t, func(ctx context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
				inputs = append(inputs, input)
				return classifierMedium(ctx, input)
			})
			upstream := &classifierResponseProvider{}
			baseline := &betaTestRouter{}
			svc := NewService(baseline, map[string]providers.Client{test.provider: upstream}, nil, false, nil, nil, false, test.provider, test.model, nil)
			require.NoError(t, svc.WithClassifierSessions(fixtureService.classifierSessions.config, store, fixtureService.classifierSessions.classifier))
			escalationClasses := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
			roster := &rosterdata.Roster{SchemaVersion: rosterdata.SchemaVersionPolicyV1, SHA256: strings.Repeat("b", 64), ClassOrder: escalationClasses, Clusters: map[string]rosterdata.Cluster{}}
			model, found := catalog.ByID(test.model)
			require.True(t, found)
			arm := armid.ForModel(model)
			for _, class := range escalationClasses {
				roster.Clusters[class] = rosterdata.Cluster{Arms: []string{arm}, ArmScores: map[string]float64{arm: 1}}
			}
			resolver := policy.NewResolver(map[string]struct{}{test.model: {}}, map[string]struct{}{test.provider: {}}, armid.ForModel, policy.ManagedProviderPolicy())
			capabilities := policy.Capabilities{SchemaVersion: policy.SchemaVersionV4, AuthoritativePerTurnSelection: true}
			routing := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyLLMClassifier, Unavailable: router.ErrClassifierUnavailable, ClassifierArtifactID: "llm-classifier-v1.0.0", ClassifierArtifactSHA256: strings.Repeat("a", 64), SelectionPolicyReleaseID: "llm-classifier-v1.0.0", SelectionPolicySHA256: roster.SHA256}, policy.AtomicClassifierFacts{Release: "llm-classifier-v1.0.0", ReleaseSHA256: strings.Repeat("a", 64)}, resolver).WithCapabilities(capabilities).WithArmSelector(selection.Selector(roster))
			svc.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyLLMClassifier, Router: routing, Capabilities: capabilities, Unavailable: router.ErrClassifierUnavailable})
			ctx := classifierAdmit(t, svc, principal)
			for _, body := range []string{test.firstBody, test.firstBody, test.nextBody} {
				upstream.body = nil
				recorder := httptest.NewRecorder()
				err := test.proxyRequest(svc, ctx, []byte(body), recorder, httptest.NewRequest(http.MethodPost, test.path, nil))
				require.NoError(t, err)
				require.NotEmpty(t, upstream.body, "selection must reach the dispatch boundary")
				require.Equal(t, test.model, recorder.Header().Get("X-Router-Model"))
			}
			require.Zero(t, baseline.calls, "enrolled traffic must never fall back to the legacy classifier")
			require.Len(t, inputs, 2)
			require.Equal(t, "first", inputs[0].User.CurrentUserMessage)
			require.Equal(t, "next", inputs[1].User.CurrentUserMessage)
			require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2}, inputs[1].User.Features)
			require.Equal(t, []router.PredictedClassifierResponse{{ResponseIndex: 0, Content: "", Complexity: router.ClassifierMedium}, {ResponseIndex: 1, Content: "answer", Complexity: router.ClassifierMedium}}, inputs[1].User.PrecedingResponses)
			toolLoop := strings.TrimSuffix(test.nextBody, "]}") + "," + test.toolActivity + "]}"
			third := strings.TrimSuffix(toolLoop, "]}") + "," + test.thirdUser + "]}"
			for index, body := range []string{toolLoop, toolLoop, third} {
				upstream.body = nil
				err := test.proxyRequest(svc, ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, test.path, nil))
				require.NoError(t, err)
				require.NotEmpty(t, upstream.body)
				if index < 2 {
					require.Len(t, inputs, 3, "each tool continuation classifies once; exact retries reuse it")
				}
			}
			require.Len(t, inputs, 4)
			require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2, ToolCallCount: 1, ToolErrorCount: 1}, inputs[2].User.Features)
			require.Equal(t, router.ClassifierFeatures{UserMessageCount: 3, ToolCallCount: 1, ToolErrorCount: 1}, inputs[3].User.Features)
			require.Equal(t, inputs[1].User.PrecedingResponses, inputs[2].User.PrecedingResponses, "tool payloads must not become response history")
			require.Zero(t, baseline.calls)
			if test.name == "messages" {
				for _, body := range []string{classifierSearchBody, classifierSearchBody, third} {
					upstream.body = nil
					err := test.proxyRequest(svc, ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, test.path, nil))
					require.NoError(t, err)
					require.NotEmpty(t, upstream.body)
					if body == classifierSearchBody {
						require.Equal(t, "web_search_20250305", gjson.GetBytes(upstream.body, "tools.0.type").String())
						require.Equal(t, gjson.Get(body, "messages.0.content").String(), gjson.GetBytes(upstream.body, "messages.0.content.0.text").String())
					}
				}
				require.Len(t, inputs, 5, "search retries reuse a child; parent resumes without reclassification")
				require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1}, inputs[4].User.Features)
				require.Zero(t, baseline.calls)
			}
			classificationCount := len(inputs)
			upstream.body = nil
			compacted := strings.ReplaceAll(test.firstBody, "first", "compacted")
			err := test.proxyRequest(svc, ctx, []byte(compacted), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, test.path, nil))
			require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
			require.Empty(t, upstream.body)
			require.Len(t, inputs, classificationCount)
			for _, failure := range []error{router.ErrClassifierInputTooLong, router.ErrClassifierUnavailable} {
				svc.classifierSessions.classifier = atomicClassifierFunc(func(context.Context, router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
					return router.ClassifierPrediction{}, failure
				})
				failureCtx := classifierAdmit(t, svc, principal)
				err := test.proxyRequest(svc, failureCtx, []byte(test.firstBody), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, test.path, nil))
				require.ErrorIs(t, err, failure)
				require.Empty(t, upstream.body, "classification failure must not dispatch a provider")
				require.Zero(t, baseline.calls)
			}
		})
	}
}

func TestClassifierInputRejectsControlBypasses(t *testing.T) {
	svc, ctx, _ := classifierSessionFixture(t, classifierMedium)
	ctx = classifierAdmit(t, svc, ctx)
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"/beta"}]}`,
		`{"messages":[{"role":"user","content":"/force-model example"}]}`,
		`{"messages":[{"role":"user","content":"first"}],"weave_handoff":"ticket"}`,
		`{"messages":[{"role":"user","content":"first"}],"weave_session":"ticket"}`,
	} {
		_, err := svc.withClassifierInput(ctx, []byte(body), router.EndpointAnthropicMessages)
		require.Error(t, err, gjson.Get(body, "messages.0.content").String())
	}
	for _, command := range []string{"/beta", "/force-model example", "/router-session", "/router-models"} {
		body := fmt.Sprintf(`{"model":"auto","input":[{"role":"user","content":%q}]}`, command)
		err := svc.ProxyOpenAIResponses(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		require.Error(t, err, command)
	}
	for _, override := range []context.Context{
		context.WithValue(ctx, AgentShadowEvalContextKey{}, AgentShadowEvaluation{Model: catalog.ModelIDGPT55.String(), RolloutID: "fixture", StateID: "fixture"}),
		router.WithPolicyPinRequest(ctx, router.PolicyPinRequest{Authorized: true, Pin: router.PolicyPin{ArtifactSHA256: strings.Repeat("a", 64), RosterSHA256: strings.Repeat("b", 64)}}),
	} {
		_, err := svc.withClassifierInput(override, []byte(`{"messages":[{"role":"user","content":"first"}]}`), router.EndpointAnthropicMessages)
		require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
	}
}

func TestClassifierUtilityRequestsDoNotEstablishThreadRoot(t *testing.T) {
	const utilityModel catalog.ModelID = "claude-sonnet-4-6"
	fixture, principal, store := classifierSessionFixture(t, classifierMedium)
	upstream := &classifierResponseProvider{}
	baseline := &betaTestRouter{}
	pins := newStubPinStore()
	svc := NewService(baseline, map[string]providers.Client{providers.ProviderAnthropic: upstream}, nil, false, nil, pins, false, providers.ProviderAnthropic, utilityModel.String(), nil)
	require.NoError(t, svc.WithClassifierSessions(fixture.classifierSessions.config, store, fixture.classifierSessions.classifier))
	ctx := classifierAdmit(t, svc, principal)
	for _, utility := range []struct {
		turn turntype.TurnType
		body string
	}{
		{turntype.Probe, `{"model":"auto","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`},
		{turntype.Classifier, `{"model":"auto","max_tokens":64,"system":"Classify the request","messages":[{"role":"user","content":"classify"}]}`},
		{turntype.TitleGen, `{"model":"auto","max_tokens":1024,"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}}},"messages":[{"role":"user","content":"title"}]}`},
		{turntype.Compaction, `{"model":"auto","max_tokens":1024,"system":"Your task is to create a detailed summary","messages":[{"role":"user","content":"summary"}]}`},
	} {
		t.Run(string(utility.turn), func(t *testing.T) {
			err := svc.ProxyMessages(ctx, []byte(utility.body), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
			status, classified := ClassifyDispatchError(err)
			require.True(t, classified)
			require.Equal(t, http.StatusConflict, status.Status)
			require.Empty(t, upstream.body)
			require.Zero(t, baseline.calls)
			for _, predictions := range store.turns {
				require.Empty(t, predictions)
			}
			pins.mu.Lock()
			defer pins.mu.Unlock()
			require.Empty(t, pins.upserts)
		})
	}
	input, err := classifierContextForCall(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "real request")))
	require.NoError(t, err)
	prediction, err := svc.classifyThread(ctx, input)
	require.NoError(t, err)
	require.Equal(t, input.RootTurnDigest, prediction.RootTurnDigest)
	require.Equal(t, router.ClassifierMedium, prediction.Complexity)
}
