package proxy_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/providers/openaicompat"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/timing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const reasoningProgressModel = string(catalog.ModelIDGPT55)

func completedReasoningFrame(index int) string {
	return fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":{"type":"reasoning","id":"rs_%d","encrypted_content":"opaque_%d","summary":[]}}`, index, index, index)
}

func flushResponsesFrame(w http.ResponseWriter, frame string) bool {
	_, err := io.WriteString(w, "data: "+frame+"\n\n")
	w.(http.Flusher).Flush()
	return err == nil
}

func TestResponsesReasoningProgress_LongReasoningCompletes(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		chat, gateway, throughput bool
	}{
		{name: "direct to anthropic"},
		{name: "direct to chat", chat: true},
		{name: "gateway to anthropic", gateway: true},
		{name: "gateway reasoning does not count as slow output", gateway: true, throughput: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logBuf := captureCompletionLog(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			ctx, stamps := timing.WithTiming(ctx)
			reasoningFinished := make(chan struct{})
			releaseAnswer := make(chan struct{})
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				assert.Equal(t, "/v1/responses", r.URL.Path)
				w.Header().Set("Content-Type", "text/event-stream")
				// 960ms is well beyond the 300ms stall budget + 100ms poll.
				for i := range 12 {
					frame := completedReasoningFrame(i)
					if tc.chat {
						frame = `{"type":"response.reasoning_text.delta","output_index":0,"delta":"working "}`
					}
					if !flushResponsesFrame(w, frame) {
						return
					}
					select {
					case <-r.Context().Done():
						return
					case <-time.After(80 * time.Millisecond):
					}
				}
				close(reasoningFinished)
				select {
				case <-r.Context().Done():
					return
				case <-releaseAnswer:
				}
				for _, frame := range []string{
					`{"type":"response.output_text.delta","output_index":12,"delta":"answer"}`,
					`{"type":"response.output_item.done","output_index":12,"item":{"type":"message","content":[{"type":"output_text","text":"answer"}]}}`,
					`{"type":"response.output_item.added","output_index":13,"item":{"type":"function_call","call_id":"call_read","name":"read_file"}}`,
					`{"type":"response.function_call_arguments.delta","output_index":13,"delta":"{\"path\":\"main.go\"}"}`,
					`{"type":"response.output_item.done","output_index":13,"item":{"type":"function_call","call_id":"call_read","name":"read_file","arguments":"{\"path\":\"main.go\"}"}}`,
					`{"type":"response.completed","response":{"id":"resp_ok","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":8}}}`,
				} {
					if !flushResponsesFrame(w, frame) {
						return
					}
				}
			}))
			defer upstream.Close()
			defer cancel()

			provider := providers.ProviderOpenAI
			var client providers.Client = openai.NewClientWithStallTimeouts("test-key", upstream.URL, time.Second, 2*time.Second, 300*time.Millisecond)
			if tc.gateway {
				provider = providers.ProviderOpenAIGateway
				client = openaicompat.NewClientWithStallTimeouts("test-key", upstream.URL+"/v1", 2*time.Second, 300*time.Millisecond)
				if tc.throughput {
					// If reasoning enters the throughput counter, two sparse
					// productive 150ms windows fail this stream before the answer.
					client = openaicompat.NewClientWithThroughputGuard("test-key", upstream.URL+"/v1", 150*time.Millisecond, 100*time.Millisecond, 100)
				}
			}
			svc := makeProxyService(router.Decision{Provider: provider, Model: reasoningProgressModel}, map[string]providers.Client{provider: client}).
				WithDeploymentKeyedProviders(map[string]struct{}{provider: {}})
			rec := httptest.NewRecorder()
			finished := make(chan error, 1)
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				if tc.chat {
					req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatToolTurnBody))
					finished <- svc.ProxyOpenAIChatCompletion(ctx, []byte(chatToolTurnBody), rec, req)
				} else {
					req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(gatewayToolTurn))
					finished <- svc.ProxyMessages(ctx, []byte(gatewayToolTurn), rec, req)
				}
			}()
			defer func() { cancel(); <-joined }()
			select {
			case err := <-finished:
				t.Fatalf("stream ended during advancing reasoning: %v", err)
			case <-ctx.Done():
				t.Fatal("reasoning phase did not finish")
			case <-reasoningFinished:
			}
			assert.Positive(t, stamps.UpstreamFirstByteNanos.Load())
			assert.Zero(t, stamps.UpstreamFirstOutputNanos.Load(), "reasoning must not stamp first output")
			close(releaseAnswer)
			select {
			case err := <-finished:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("stream did not complete")
			}
			assert.EqualValues(t, 1, attempts.Load())
			assert.Positive(t, stamps.UpstreamFirstOutputNanos.Load())
			body := rec.Body.String()
			assert.Contains(t, body, "answer")
			assert.Contains(t, body, `"name":"read_file"`)
			assert.Contains(t, body, `\"path\":\"main.go\"`)
			assert.NotContains(t, body, `"type":"error"`)
			assert.NotContains(t, logBuf.String(), "stall_kind=")
			if tc.chat {
				assert.Contains(t, body, `"reasoning_content":"working "`)
				assert.Contains(t, body, `"finish_reason":"tool_calls"`)
				assert.Contains(t, body, "data: [DONE]")
			} else {
				signatures := 0
				for _, line := range strings.Split(body, "\n") {
					payload := strings.TrimPrefix(line, "data: ")
					if gjson.Get(payload, "delta.type").String() != "signature_delta" {
						continue
					}
					envelope, err := base64.StdEncoding.DecodeString(gjson.Get(payload, "delta.signature").String())
					require.NoError(t, err)
					assert.Equal(t, fmt.Sprintf("rs_%d", signatures), gjson.GetBytes(envelope, "id").String())
					assert.Equal(t, fmt.Sprintf("opaque_%d", signatures), gjson.GetBytes(envelope, "enc").String())
					signatures++
				}
				assert.Equal(t, 12, signatures, "every opaque reasoning item must survive in order")
				assert.Less(t, strings.LastIndex(body, "signature_delta"), strings.Index(body, `"text":"answer"`))
				assert.Contains(t, body, "event: message_stop")
			}
		})
	}
}

func TestResponsesReasoningProgress_StallAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, frame, failureClass, stallKind                         string
		initialReasoning, continuousReasoning, silence, cancelCaller bool
		deadline                                                     time.Duration
	}{
		{name: "status and ping only", frame: `{"type":"response.in_progress"}`, stallKind: "output_idle"},
		{name: "empty deltas", frame: `{"type":"response.reasoning_text.delta","delta":""}`, stallKind: "output_idle"},
		{name: "empty items", frame: `{"type":"response.output_item.done","item":{"type":"reasoning","summary":[]}}`, stallKind: "output_idle"},
		{name: "reasoning then keepalives cannot replay", initialReasoning: true, frame: `{"type":"response.in_progress"}`, failureClass: "output_stall_watchdog", stallKind: "output_idle"},
		{name: "byte silence", initialReasoning: true, silence: true, failureClass: "idle_watchdog", stallKind: "byte_idle"},
		{name: "parent deadline", initialReasoning: true, continuousReasoning: true, deadline: 900 * time.Millisecond, failureClass: "deadline"},
		{name: "caller cancellation", initialReasoning: true, continuousReasoning: true, cancelCaller: true, failureClass: "client_canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logBuf := captureCompletionLog(t)
			budget := 5 * time.Second
			if tc.deadline > 0 {
				budget = tc.deadline
			}
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			var attempts atomic.Int32
			var lastReasoningAtNanos atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				if tc.initialReasoning {
					// Progress after 200ms proves that the stall clock resets,
					// rather than measuring only time since response headers.
					for i := range 3 {
						lastReasoningAtNanos.Store(time.Now().UnixNano())
						if !flushResponsesFrame(w, completedReasoningFrame(i)) {
							return
						}
						select {
						case <-r.Context().Done():
							return
						case <-time.After(100 * time.Millisecond):
						}
					}
				}
				if tc.cancelCaller {
					timer := time.AfterFunc(450*time.Millisecond, cancel)
					defer timer.Stop()
				}
				if tc.silence {
					<-r.Context().Done()
					return
				}
				for index := 3; ; index++ {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(50 * time.Millisecond):
					}
					frame := tc.frame
					if tc.continuousReasoning {
						frame = completedReasoningFrame(index)
					}
					if !flushResponsesFrame(w, frame) {
						return
					}
					if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
						return
					}
					w.(http.Flusher).Flush()
				}
			}))
			defer upstream.Close()
			defer cancel()
			idle, stall := 2*time.Second, 300*time.Millisecond
			if tc.silence {
				idle, stall = 250*time.Millisecond, 2*time.Second
			}
			client := openai.NewClientWithStallTimeouts("test-key", upstream.URL, time.Second, idle, stall)
			svc := makeProxyService(router.Decision{Provider: providers.ProviderOpenAI, Model: reasoningProgressModel}, map[string]providers.Client{providers.ProviderOpenAI: client})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(gatewayToolTurn))
			started := time.Now()
			err := svc.ProxyMessages(ctx, []byte(gatewayToolTurn), rec, req)
			elapsed := time.Since(started)
			require.Error(t, err)
			assert.Less(t, elapsed, 3*time.Second, "must terminate before the safety deadline")
			if tc.initialReasoning {
				assert.EqualValues(t, 1, attempts.Load(), "thinking-only commitment prohibits replay")
				assert.Contains(t, rec.Body.String(), "signature_delta")
				assert.Contains(t, rec.Body.String(), "event: error")
				assert.NotContains(t, rec.Body.String(), "event: message_stop")
				assert.Contains(t, logBuf.String(), "stream_failure_class="+tc.failureClass)
				if tc.stallKind == "output_idle" {
					assert.GreaterOrEqual(t, time.Since(time.Unix(0, lastReasoningAtNanos.Load())), stall)
				}
			}
			if tc.stallKind != "" {
				assert.Contains(t, logBuf.String(), "stall_kind="+tc.stallKind)
			}
			if tc.continuousReasoning {
				assert.GreaterOrEqual(t, elapsed, 700*time.Millisecond)
				assert.NotContains(t, logBuf.String(), "stall_kind=")
			}
			assert.NotContains(t, logBuf.String(), "aborting for retry")
		})
	}
}
