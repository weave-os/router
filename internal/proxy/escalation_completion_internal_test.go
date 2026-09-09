package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/cache"
	"weave-os/router/internal/router/policy"
)

func escalationCompletionContext() context.Context {
	ctx := escalationTestContext(true, false)
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
	return context.WithValue(ctx, APIKeyIDContextKey{}, "completion-test-key")
}

func newEscalationCompletionService(store *escalationTestStore, observer *escalationTestObserver, clients map[string]providers.Client) *Service {
	return NewService(nil, clients, nil, false, nil, newStubPinStore(), false, providers.ProviderAnthropic, "claude-opus-4-8", nil).
		WithEscalation(store, observer).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: escalationDispatchRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: true}})
}

func TestEscalationCompletionRecordsPostRoutingPreparationFailure(t *testing.T) {
	for _, tc := range []struct {
		name          string
		body          string
		invoke        func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
		expectedError error
	}{
		{"messages_missing_provider", `{"model":"claude-opus-4-8","max_tokens":4096,"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"inspect the repository"}]}`, (*Service).ProxyMessages, ErrProviderNotConfigured},
		{"chat_missing_provider", `{"model":"gpt-5","messages":[{"role":"user","content":"inspect the repository"}],"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]}`, (*Service).ProxyOpenAIChatCompletion, ErrProviderNotConfigured},
		{"responses_missing_provider", `{"model":"gpt-5","input":"inspect the repository","tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}]}`, (*Service).ProxyOpenAIResponses, ErrProviderNotConfigured},
		{"gemini_cross_format", `{"model":"gemini-3-pro-preview","contents":[{"role":"user","parts":[{"text":"inspect the repository"}]}],"tools":[{"functionDeclarations":[{"name":"Read","parameters":{"type":"object"}}]}]}`, (*Service).ProxyGeminiGenerateContent, ErrGeminiCrossFormatUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newEscalationTestStore()
			observer := &escalationTestObserver{}
			clients := map[string]providers.Client{providers.ProviderGoogle: &stripFailureProvider{}}
			svc := newEscalationCompletionService(store, observer, clients)
			err := tc.invoke(svc, escalationCompletionContext(), []byte(tc.body), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/test", nil))
			require.ErrorIs(t, err, tc.expectedError)
			require.Len(t, observer.requests, 1, "request must reach escalation before failing preparation")
			require.Len(t, store.sessions, 1)
			for _, session := range store.sessions {
				require.EqualValues(t, 1, session.Ordinal)
				require.NotNil(t, session.PreviousOutcome, "the committed observation must retain the failed request outcome")
				require.True(t, session.PreviousOutcome.IsError)
				require.Equal(t, http.StatusInternalServerError, session.PreviousOutcome.StatusCode)
			}
		})
	}
}

type escalationCompletionProvider struct {
	writeResponse func(http.ResponseWriter) error
}

func (p escalationCompletionProvider) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	return p.writeResponse(w)
}
func (p escalationCompletionProvider) Passthrough(_ context.Context, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	return p.writeResponse(w)
}

type escalationFinalizeFailureWriter struct {
	*httptest.ResponseRecorder
	writeErr    error
	beforeWrite func()
}

func (w *escalationFinalizeFailureWriter) Write(body []byte) (int, error) {
	w.beforeWrite()
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(body)
}

func TestEscalationCompletionWaitsForResponsesFinalization(t *testing.T) {
	for _, failFinalize := range []bool{false, true} {
		name := "successful_finalization"
		if failFinalize {
			name = "failed_finalization"
		}
		t.Run(name, func(t *testing.T) {
			store := newEscalationTestStore()
			observer := &escalationTestObserver{}
			provider := escalationCompletionProvider{writeResponse: func(w http.ResponseWriter) error {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"repository inspected"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}}`))
				return err
			}}
			svc := newEscalationCompletionService(store, observer, map[string]providers.Client{providers.ProviderAnthropic: provider})
			finalizationErr := errors.New("response client disconnected during finalization")
			writes := 0
			writer := &escalationFinalizeFailureWriter{ResponseRecorder: httptest.NewRecorder(), beforeWrite: func() {
				writes++
				require.Len(t, store.sessions, 1)
				for _, session := range store.sessions {
					require.Nil(t, session.PreviousOutcome, "completion must wait for Responses Finalize to finish")
				}
			}}
			if failFinalize {
				writer.writeErr = finalizationErr
			}
			body := []byte(`{"model":"gpt-5","input":"inspect the repository","tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}]}`)
			err := svc.ProxyOpenAIResponses(escalationCompletionContext(), body, writer, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			if failFinalize {
				require.ErrorIs(t, err, finalizationErr)
			} else {
				require.NoError(t, err)
			}
			require.Positive(t, writes)
			require.Len(t, observer.requests, 1)
			for _, session := range store.sessions {
				require.NotNil(t, session.PreviousOutcome)
				require.Equal(t, failFinalize, session.PreviousOutcome.IsError)
			}
			if failFinalize {
				require.Empty(t, store.continuations)
			} else {
				require.Contains(t, writer.Body.String(), "repository inspected")
				require.Len(t, store.continuations, 1, "completed Responses output must remain available for continuation")
			}
		})
	}
}

func TestEscalationTurnsBypassPopulatedSemanticCache(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format cache.Format
		body   string
		invoke func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", cache.FormatAnthropic, `{"model":"claude-opus-4-8","max_tokens":4096,"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"inspect the repository"}]}`, (*Service).ProxyMessages},
		{"chat", cache.FormatOpenAI, `{"model":"gpt-5","messages":[{"role":"user","content":"inspect the repository"}],"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]}`, (*Service).ProxyOpenAIChatCompletion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newEscalationTestStore()
			semanticCache := cache.New(cache.DefaultConfig())
			embedding := []float32{1, 0}
			const externalID = "escalation-cache-test"
			semanticCache.Store(externalID, tc.format, embedding, 1, cache.CachedResponse{StatusCode: http.StatusOK, Body: []byte(`{"cached":true}`)}, "", 0)
			_, hit := semanticCache.Lookup(externalID, tc.format, embedding, []int{1}, "", 0)
			require.True(t, hit)
			classifier := &authoritativeTestRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5", Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMMEmbedding), Embedding: embedding, ClusterIDs: []int{1}}}}
			svc := NewService(nil, nil, nil, false, semanticCache, newStubPinStore(), false, providers.ProviderAnthropic, "claude-opus-4-8", nil).WithEscalation(store, &escalationTestObserver{}).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: classifier, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1}})
			ctx := context.WithValue(escalationCompletionContext(), ExternalIDContextKey{}, externalID)
			recorder := httptest.NewRecorder()
			err := tc.invoke(svc, ctx, []byte(tc.body), recorder, httptest.NewRequest(http.MethodPost, "/test", nil))
			require.ErrorIs(t, err, ErrProviderNotConfigured, "an opted-in observation must attempt dispatch instead of returning cached content")
			require.NotContains(t, recorder.Body.String(), "cached")
			require.Len(t, store.sessions, 1)
			for _, session := range store.sessions {
				require.NotNil(t, session.PreviousOutcome)
				require.True(t, session.PreviousOutcome.IsError)
			}
		})
	}
}
