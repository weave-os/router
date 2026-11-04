package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiUsageAcrossClientSurfaces(t *testing.T) {
	const response = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"thoughtsTokenCount":3,"cachedContentTokenCount":80,"totalTokenCount":110}}`
	for _, stream := range []bool{false, true} {
		for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses, translate.FormatGemini} {
			t.Run(fmt.Sprintf("%v/stream=%v", format, stream), func(t *testing.T) {
				telemetry := newCaptureTelemetry()
				provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
					body, contentType := response, "application/json"
					if stream {
						body = "data: " + response + "\n\n"
						contentType = "text/event-stream"
					}
					w.Header().Set("Content-Type", contentType)
					w.WriteHeader(http.StatusOK)
					_, err := w.Write([]byte(body))
					require.NoError(t, err)
				}}
				svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: router.ModelGemini4Argon}}, map[string]providers.Client{providers.ProviderGoogle: provider}, nil, false, nil, nil, false, providers.ProviderGoogle, router.ModelGemini4Argon, telemetry)
				ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, "55555555-5555-5555-5555-555555555555")
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.Header.Set(routingMarkerHeader, "off")
				var err error
				switch format {
				case translate.FormatAnthropic:
					err = svc.ProxyMessages(ctx, []byte(fmt.Sprintf(`{"model":"auto","stream":%v,"messages":[{"role":"user","content":"hello"}]}`, stream)), rec, req)
				case translate.FormatOpenAI:
					err = svc.ProxyOpenAIChatCompletion(ctx, []byte(fmt.Sprintf(`{"model":"auto","stream":%v,"messages":[{"role":"user","content":"hello"}]}`, stream)), rec, req)
				case translate.FormatOpenAIResponses:
					err = svc.ProxyOpenAIResponses(ctx, []byte(fmt.Sprintf(`{"model":"auto","stream":%v,"input":"hello"}`, stream)), rec, req)
				case translate.FormatGemini:
					req = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+router.ModelGemini4Argon+":generateContent", nil)
					if stream {
						req = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+router.ModelGemini4Argon+":streamGenerateContent", nil)
					}
					req.Header.Set(routingMarkerHeader, "off")
					err = svc.ProxyGeminiGenerateContent(ctx, []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`), rec, req)
				}
				require.NoError(t, err)
				row := telemetry.firstRow(t)
				assert.Equal(t, int32(100), row.InputTokens)
				assert.Equal(t, int32(10), row.OutputTokens, "thoughts must contribute exactly once")
				require.NotNil(t, row.CacheReadTokens)
				assert.Equal(t, int32(80), *row.CacheReadTokens)
				require.NotNil(t, row.ReasoningTokens)
				assert.Equal(t, int32(3), *row.ReasoningTokens)
				body := rec.Body.String()
				if format == translate.FormatGemini {
					assert.Contains(t, body, `"cachedContentTokenCount":80`)
					return
				}
				var object gjson.Result
				if stream {
					for _, line := range strings.Split(body, "\n") {
						if payload, ok := strings.CutPrefix(line, "data: "); ok {
							root := gjson.Parse(payload)
							if root.Get("type").String() == "response.completed" {
								object = root.Get("response.usage")
							} else if u := root.Get("usage"); u.IsObject() {
								object = u
							}
						}
					}
				} else {
					object = gjson.Get(body, "usage")
				}
				cachePath, outputPath, reasoningPath := "prompt_tokens_details.cached_tokens", "completion_tokens", "completion_tokens_details.reasoning_tokens"
				if format == translate.FormatAnthropic {
					cachePath = "cache_read_input_tokens"
					outputPath = "output_tokens"
				}
				if format == translate.FormatOpenAIResponses {
					cachePath = "input_tokens_details.cached_tokens"
					outputPath = "output_tokens"
					reasoningPath = "output_tokens_details.reasoning_tokens"
				}
				assert.EqualValues(t, 80, object.Get(cachePath).Int())
				assert.EqualValues(t, 10, object.Get(outputPath).Int())
				if format != translate.FormatAnthropic {
					assert.EqualValues(t, 3, object.Get(reasoningPath).Int())
				}
			})
		}
	}
}

func TestUsageProjection_XAIStreamCostIncludesReasoningOnce(t *testing.T) {
	const model = "grok-4.6"
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":7,\"completion_tokens_details\":{\"reasoning_tokens\":3}}}\n\ndata: [DONE]\n\n"))
		require.NoError(t, err)
	}}
	telemetry := newCaptureTelemetry()
	svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderXAI, Model: model}}, map[string]providers.Client{providers.ProviderXAI: provider}, nil, false, nil, nil, false, providers.ProviderXAI, model, telemetry)
	ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, "55555555-5555-5555-5555-555555555555")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set(routingMarkerHeader, "off")
	require.NoError(t, svc.ProxyMessages(ctx, []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`), rec, req))
	row := telemetry.firstRow(t)
	assert.Equal(t, int32(10), row.OutputTokens)
	var outputCost gjson.Result
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(payload, "type").String() == "message_delta" {
			outputCost = gjson.Get(payload, "usage.weave_cost.output_usd")
		}
	}
	require.True(t, outputCost.Exists())
	assert.InDelta(t, 0.00006, outputCost.Float(), 1e-12, "ten output tokens at $6/M, including reasoning exactly once")
}

func TestUsageProjection_NoticesAndFeedbackContributeNoProviderUsage(t *testing.T) {
	const response = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
	telemetry := newCaptureTelemetry()
	provider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("data: " + response + "\n\n"))
		require.NoError(t, err)
	}}
	svc := proxy.NewService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: router.ModelGemini4Argon}}, map[string]providers.Client{providers.ProviderGoogle: provider}, nil, false, nil, nil, false, providers.ProviderGoogle, router.ModelGemini4Argon, telemetry).WithRouterFeedbackStore(&fakeFeedbackStore{})
	ctx := context.WithValue(context.Background(), proxy.InstallationIDContextKey{}, "55555555-5555-5555-5555-555555555555")
	ctx = context.WithValue(ctx, proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{ClientApp: proxy.ClientAppClaudeCode})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`), rec, req))
	assert.Contains(t, rec.Body.String(), "Weave Router")
	assert.Contains(t, rec.Body.String(), "/rf")
	row := telemetry.firstRow(t)
	assert.Zero(t, row.InputTokens)
	assert.Zero(t, row.OutputTokens)
	assert.Nil(t, row.CacheReadTokens)
	assert.Nil(t, row.ReasoningTokens)
}
