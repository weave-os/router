package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Source completion facts must survive the Chat-to-Messages conversion, which
// maps unsupported or absent Chat finish reasons to Anthropic end_turn.
func TestFeedbackCompletion_MessagesRetainsSourceChatEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		finish string
		want   int
	}{
		{"natural answer", `"stop"`, 1},
		{"content filtered", `"content_filter"`, 0},
		{"missing finish", "null", 0},
		{"truncated", `"length"`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+`{"id":"c1","choices":[{"index":0,"delta":{"content":"synthetic answer"},"finish_reason":`+tc.finish+`}]}`+"\n\ndata: [DONE]\n\n")
			}}
			svc := feedbackCompletionService(provider, providers.ProviderOpenRouter, "qwen/qwen3-coder", nil)
			ctx := context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode})
			body := []byte(`{"model":"auto","stream":true,"max_tokens":1024,"tools":[{"name":"read_file","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"explain the synthetic example"}]}`)
			recorder := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(ctx, body, recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))

			require.Equal(t, []providers.Endpoint{providers.EndpointChatCompletions}, provider.proxyEndpoints)
			assert.Contains(t, recorder.Body.String(), "synthetic answer")
			assert.Equal(t, tc.want, strings.Count(recorder.Body.String(), "_Weave Router feedback:_"))
			if tc.want > 0 {
				assert.Less(t, strings.Index(recorder.Body.String(), "synthetic answer"), strings.Index(recorder.Body.String(), "_Weave Router feedback:_"))
				assert.Less(t, strings.Index(recorder.Body.String(), "_Weave Router feedback:_"), strings.Index(recorder.Body.String(), "event: message_stop"))
			}
		})
	}
}

func TestFeedbackCompletion_BufferedResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		model    string
		response string
		endpoint providers.Endpoint
		want     int
	}{
		{
			name: "native natural answer", provider: providers.ProviderOpenAI, model: "gpt-5.6-sol", endpoint: providers.EndpointResponses, want: 1,
			response: `{"id":"resp_synthetic","object":"response","status":"completed","output":[{"id":"msg_synthetic","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"synthetic answer","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":3}}`,
		},
		{
			name: "converted natural answer", provider: providers.ProviderAnthropic, model: "claude-haiku-4-5", endpoint: providers.EndpointChatCompletions, want: 1,
			response: `{"id":"msg_synthetic","type":"message","role":"assistant","content":[{"type":"text","text":"synthetic answer"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":3}}`,
		},
		{
			name: "converted truncation", provider: providers.ProviderAnthropic, model: "claude-haiku-4-5", endpoint: providers.EndpointChatCompletions,
			response: `{"id":"msg_synthetic","type":"message","role":"assistant","content":[{"type":"text","text":"synthetic answer"}],"stop_reason":"max_tokens","usage":{"input_tokens":12,"output_tokens":3}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}}
			telemetry := newCaptureTelemetry()
			var captured string
			svc := feedbackCompletionService(provider, tc.provider, tc.model, telemetry).
				WithContentCapture(proxy.CaptureFull, 1<<20, func(content string, kind proxy.ContentKind) string {
					if kind == proxy.ContentKindResponse {
						captured = content
					}
					return content
				})
			ctx := context.WithValue(authedCtx(nativeResponsesInstallationID), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
			body := []byte(`{"model":"auto","stream":false,"input":"explain the synthetic example"}`)
			recorder := httptest.NewRecorder()
			require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))

			require.Equal(t, []providers.Endpoint{tc.endpoint}, provider.proxyEndpoints)
			text := gjson.GetBytes(recorder.Body.Bytes(), "output.0.content.0.text").String()
			assert.Contains(t, text, "synthetic answer")
			assert.Equal(t, tc.want, strings.Count(text, "_Weave Router feedback:_"))
			if tc.want > 0 {
				assert.Contains(t, text, "$rf +")
				assert.NotContains(t, text, "/rf")
			}
			row := telemetry.firstRow(t)
			assert.EqualValues(t, 12, row.InputTokens)
			assert.EqualValues(t, 3, row.OutputTokens)
			assert.NotContains(t, string(provider.proxyBodies[0]), "_Weave Router feedback:_")
			assert.Contains(t, captured, "synthetic answer")
			assert.NotContains(t, captured, "_Weave Router feedback:_", "response capture must remain upstream of downstream feedback insertion")
		})
	}
}

func feedbackCompletionService(provider providers.Client, providerName, model string, telemetry proxy.TelemetryRepository) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providerName, Model: model, Reason: "test"}},
		map[string]providers.Client{providerName: provider},
		nil, false, nil, nil, false, providerName, model, telemetry,
	).WithRouterFeedbackStore(&fakeFeedbackStore{})
}

// A request-scoped delivery survives pre-content provider retries while each
// new observer discards the failed attempt's completion evidence.
func TestFeedbackCompletion_RetryDeliversOnce(t *testing.T) {
	provider := &feedbackRetryProvider{fakeProvider: fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_synthetic","object":"response","status":"completed","output":[{"id":"msg_synthetic","type":"message","role":"assistant","content":[{"type":"output_text","text":"served after retry"}]}],"usage":{"input_tokens":12,"output_tokens":3}}`)
	}}}
	telemetry := newCaptureTelemetry()
	var captured string
	svc := feedbackCompletionService(provider, providers.ProviderOpenAI, "gpt-5.6-sol", telemetry).
		WithContentCapture(proxy.CaptureFull, 1<<20, func(content string, kind proxy.ContentKind) string {
			if kind == proxy.ContentKindResponse {
				captured = content
			}
			return content
		})
	ctx := context.WithValue(authedCtx(nativeResponsesInstallationID), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppCodex})
	body := []byte(`{"model":"auto","stream":false,"input":"explain the synthetic example"}`)
	recorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))

	assert.Equal(t, 2, provider.attempts)
	assert.Equal(t, 1, strings.Count(recorder.Body.String(), "_Weave Router feedback:_"))
	assert.Contains(t, recorder.Body.String(), "served after retry")
	assert.NotContains(t, recorder.Body.String(), "overloaded")
	row := telemetry.firstRow(t)
	assert.EqualValues(t, 12, row.InputTokens)
	assert.EqualValues(t, 3, row.OutputTokens)
	assert.Contains(t, captured, "served after retry")
	assert.NotContains(t, captured, "_Weave Router feedback:_")
	for _, upstreamBody := range provider.proxyBodies {
		assert.NotContains(t, string(upstreamBody), "_Weave Router feedback:_")
	}
}

type feedbackRetryProvider struct {
	fakeProvider
	attempts int
}

func (p *feedbackRetryProvider) Proxy(ctx context.Context, decision router.Decision, prepared providers.PreparedRequest, w http.ResponseWriter, request *http.Request) error {
	p.attempts++
	if p.attempts == 1 {
		return &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":{"message":"overloaded"}}`)}
	}
	return p.fakeProvider.Proxy(ctx, decision, prepared, w, request)
}
