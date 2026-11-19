package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/websearch"
)

func (s *Service) serveNativeWebSearch(
	ctx context.Context,
	body []byte,
	model string,
	stream bool,
	inputTokens int,
	enabled map[string]struct{},
	headers http.Header,
	w http.ResponseWriter,
) (served, gatewayAttempted bool) {
	if s.webSearch.gateway == nil {
		return false, false
	}
	tool, hasTool := websearch.FindServerTool(body)
	if !hasTool || anyNativeServerToolProvider(enabled) {
		return false, false
	}
	log := observability.FromContext(ctx)
	query, isSearchTurn := websearch.DetectSearchTurn(body)
	if !isSearchTurn {
		log.Info("Native web-search tool on a provider that cannot serve it; turn left on normal routing",
			"tool_type", tool.Type)
		return false, false
	}

	credCtx := resolveAndInjectCredentials(ctx, providers.ProviderAnthropicGateway, model, headers)
	if CredentialsFromContext(credCtx) == nil {
		log.Info("Native web-search executor skipped: no gateway credential for this request")
		return false, false
	}

	start := s.clock.Now()
	result, err := s.webSearch.gateway.Search(credCtx, query)
	if err != nil {
		log.Warn("Native web-search execution failed; falling back to normal routing",
			"err", err, "latency_ms", s.clock.Now().Sub(start).Milliseconds())
		return false, true
	}

	msg := websearch.SynthesizeMessage(
		"msg_router_websearch_"+fmt.Sprintf("%x", s.clock.Now().UnixNano()),
		model, tool.Name, query, result, inputTokens,
	)
	if err := writeWebSearchMessage(w, msg, stream); err != nil {
		log.Warn("Failed writing synthesized web-search response", "err", err)
		return true, true
	}
	log.Info("Served native web-search turn on the tenant's own provider",
		"tool_type", tool.Type,
		"results", len(result.Results),
		"latency_ms", s.clock.Now().Sub(start).Milliseconds(),
	)
	return true, true
}

type webSearchExecutors struct {
	gateway  websearch.Executor
	fallback websearch.Executor
}

type messagesWebSearchTurn struct {
	body        []byte
	model       string
	stream      bool
	inputTokens int
	enabled     map[string]struct{}
}

func (t messagesWebSearchTurn) serve(ctx context.Context, s *Service, r *http.Request, w http.ResponseWriter) (bool, error) {
	if preparingHandoff(ctx) {
		return false, nil
	}
	served, gatewayAttempted := s.serveNativeWebSearch(ctx, t.body, t.model, t.stream, t.inputTokens, t.enabled, r.Header, w)
	if served {
		return true, nil
	}
	if handled, err := s.serveCodexWebSearch(ctx, t.body, t.model, t.stream, t.enabled, r, w); handled {
		return true, err
	}
	if gatewayAttempted {
		return false, nil
	}
	return t.serveFallback(ctx, s.webSearch.fallback, s.clock, w), nil
}

func (t messagesWebSearchTurn) serveFallback(ctx context.Context, executor websearch.Executor, clock auth.Clock, w http.ResponseWriter) bool {
	if executor == nil || anyNativeServerToolProvider(t.enabled) || !websearch.IsClaudeCodeWebSearchHelper(t.body) {
		return false
	}
	tool, _ := websearch.FindServerTool(t.body)
	log := observability.FromContext(ctx)
	if websearch.HasDomainFilter(t.body) {
		log.Info("Fallback web-search executor skipped: domain-filtered search left on normal routing",
			"tool_type", tool.Type)
		return false
	}

	query, _ := websearch.DetectSearchTurn(t.body)
	start := clock.Now()
	result, err := executor.Search(ctx, query)
	if err != nil {
		log.Warn("Fallback web-search execution failed; falling back to normal routing",
			"err", err, "latency_ms", clock.Now().Sub(start).Milliseconds())
		return false
	}

	msg := websearch.SynthesizeMessage(
		"msg_router_websearch_"+fmt.Sprintf("%x", clock.Now().UnixNano()),
		t.model, tool.Name, query, result, t.inputTokens,
	)
	if err := writeWebSearchMessage(w, msg, t.stream); err != nil {
		log.Warn("Failed writing synthesized fallback web-search response", "err", err)
		return true
	}
	log.Info("Served web-search turn on the fallback search executor",
		"tool_type", tool.Type,
		"results", len(result.Results),
		"latency_ms", clock.Now().Sub(start).Milliseconds(),
	)
	return true
}

func anyNativeServerToolProvider(enabled map[string]struct{}) bool {
	for provider := range enabled {
		if providers.SupportsAnthropicServerTools(provider) {
			return true
		}
	}
	return false
}

func writeWebSearchMessage(w http.ResponseWriter, msg websearch.Message, stream bool) error {
	if stream {
		return writeWebSearchSSE(w, msg)
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal web-search response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, writeErr := w.Write(body)
	return writeErr
}

func writeWebSearchSSE(w http.ResponseWriter, msg websearch.Message) error {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	bw := bufio.NewWriterSize(w, 8192)
	initialUsage := msg.Usage
	initialUsage.OutputTokens = 0

	bw.WriteString(sseEvent("message_start", mustMarshalJSON(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msg.ID, "type": "message", "role": "assistant",
			"content": []any{}, "model": msg.Model,
			"stop_reason": nil, "stop_sequence": nil,
			"usage": initialUsage,
		},
	})))

	for i, block := range msg.Content {
		switch block.Type {
		case "server_tool_use":
			bw.WriteString(sseEvent("content_block_start", mustMarshalJSON(map[string]any{
				"type": "content_block_start", "index": i,
				"content_block": map[string]any{
					"type": block.Type, "id": block.ID, "name": block.Name, "input": map[string]any{},
				},
			})))
			bw.WriteString(sseEvent("content_block_delta", mustMarshalJSON(map[string]any{
				"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": mustMarshalJSON(block.Input)},
			})))
		case "text":
			bw.WriteString(sseEvent("content_block_start", mustMarshalJSON(map[string]any{
				"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "text", "text": ""},
			})))
			bw.WriteString(sseEvent("content_block_delta", mustMarshalJSON(map[string]any{
				"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "text_delta", "text": block.Text},
			})))
		default:
			bw.WriteString(sseEvent("content_block_start", mustMarshalJSON(map[string]any{
				"type": "content_block_start", "index": i, "content_block": block,
			})))
		}
		bw.WriteString(sseEvent("content_block_stop", mustMarshalJSON(map[string]any{
			"type": "content_block_stop", "index": i,
		})))
	}

	bw.WriteString(sseEvent("message_delta", mustMarshalJSON(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": msg.StopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": msg.Usage.OutputTokens},
	})))
	bw.WriteString(sseEvent("message_stop", `{"type":"message_stop"}`))

	if err := bw.Flush(); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}
