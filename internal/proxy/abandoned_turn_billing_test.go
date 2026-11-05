package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const abandonedBillingAnthropicModel = "claude-sonnet-4-6"

const abandonedBillingAnthropicStart = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":4}}}` +
	"\n\n"

const abandonedBillingAnthropicStartWithoutUsage = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-sonnet-4-6","content":[]}}` +
	"\n\n"

const abandonedBillingAnthropicText = "event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` +
	"\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial answer"}}` +
	"\n\n"

const abandonedBillingAnthropicComplete = "event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` +
	"\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` +
	"\n\n"

type abandonedBillingProxyCall func(*proxy.Service, context.Context, []byte, http.ResponseWriter, *http.Request) error

func TestAbandonBillsReportedUsageAcrossProxySurfaces(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		body     string
		path     string
		call     abandonedBillingProxyCall
		write    func(http.ResponseWriter)
	}{
		{
			name:     "Anthropic Messages",
			provider: providers.ProviderAnthropic,
			model:    abandonedBillingAnthropicModel,
			body:     `{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			path:     "/v1/messages",
			call:     (*proxy.Service).ProxyMessages,
			write:    writeAbandonedBillingAnthropicStream,
		},
		{
			name:     "OpenAI chat routed to Anthropic",
			provider: providers.ProviderAnthropic,
			model:    abandonedBillingAnthropicModel,
			body:     `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			path:     "/v1/chat/completions",
			call:     (*proxy.Service).ProxyOpenAIChatCompletion,
			write:    writeAbandonedBillingAnthropicStream,
		},
		{
			name:     "OpenAI Responses routed to Anthropic",
			provider: providers.ProviderAnthropic,
			model:    abandonedBillingAnthropicModel,
			body:     `{"model":"auto","stream":true,"input":[{"role":"user","content":"hi"}]}`,
			path:     "/v1/responses",
			call:     (*proxy.Service).ProxyOpenAIResponses,
			write:    writeAbandonedBillingAnthropicStream,
		},
		{
			name:     "Gemini",
			provider: providers.ProviderGoogle,
			model:    "gemini-2.5-pro",
			body:     `{"model":"gemini-2.5-pro","stream":true,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			path:     "/v1beta/models/gemini-2.5-pro:streamGenerateContent",
			call:     (*proxy.Service).ProxyGeminiGenerateContent,
			write: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":1,"totalTokenCount":13,"cachedContentTokenCount":4}}`+"\n\n")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, "org-test")
			repo := &capturingBillingRepo{}
			upstream := &fakeProvider{
				proxyErr: context.Canceled,
				proxyResponse: func(w http.ResponseWriter) {
					tt.write(w)
					cancel()
				},
			}
			svc := abandonedBillingService(tt.provider, tt.model, upstream, repo)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))

			_ = tt.call(svc, ctx, []byte(tt.body), rec, req)

			debits := repo.recordedDebits()
			require.Len(t, debits, 1, "reported upstream usage must be billed once")
			assert.Equal(t, tt.model, debits[0].RouterModel)
			assert.Positive(t, debits[0].NotionalCostMicros)
		})
	}
}

func TestAbandonBillingNegativeCases(t *testing.T) {
	tests := []struct {
		name         string
		upstreamErr  error
		cancelClient bool
		frames       string
		wantDebits   int
	}{
		{
			name:         "canceled before usage",
			upstreamErr:  context.Canceled,
			cancelClient: true,
			frames:       abandonedBillingAnthropicStartWithoutUsage + abandonedBillingAnthropicText,
		},
		{
			name:        "unexpected EOF without client cancellation",
			upstreamErr: io.ErrUnexpectedEOF,
			frames:      abandonedBillingAnthropicStart + abandonedBillingAnthropicText,
		},
		{
			name:       "completed stream",
			frames:     abandonedBillingAnthropicStart + abandonedBillingAnthropicText + abandonedBillingAnthropicComplete,
			wantDebits: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = context.WithValue(ctx, proxy.ExternalIDContextKey{}, "org-test")
			repo := &capturingBillingRepo{}
			upstream := &fakeProvider{
				proxyErr: tt.upstreamErr,
				proxyResponse: func(w http.ResponseWriter) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, tt.frames)
					if tt.cancelClient {
						cancel()
					}
				},
			}
			svc := abandonedBillingService(providers.ProviderAnthropic, abandonedBillingAnthropicModel, upstream, repo)
			body := `{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hi"}]}`

			_ = svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(),
				httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))

			assert.Len(t, repo.recordedDebits(), tt.wantDebits)
		})
	}
}

func abandonedBillingService(provider, model string, upstream *fakeProvider, repo *capturingBillingRepo) *proxy.Service {
	return proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: provider, Model: model}},
		map[string]providers.Client{provider: upstream},
		nil, false, nil, nil, false, provider, model, nil,
	).WithBillingService(billing.NewService(repo))
}

func writeAbandonedBillingAnthropicStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, abandonedBillingAnthropicStart+abandonedBillingAnthropicText)
}
