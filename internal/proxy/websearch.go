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
	"weave-os/router/internal/subscriptions"
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
) bool {
	if s.webSearch == nil && s.fallbackWebSearch == nil {
		return false
	}
	tool, hasTool := websearch.FindServerTool(body)
	if !hasTool || anyNativeServerToolProvider(enabled) {
		return false
	}
	log := observability.FromContext(ctx)
	query, isSearchTurn := websearch.DetectSearchTurn(body)
	if !isSearchTurn {
		log.Info("Native web-search tool on a provider that cannot serve it; turn left on normal routing",
			"tool_type", tool.Type)
		return false
	}

	if s.webSearch == nil {
		return fallbackWebSearchTurn{
			executor: s.fallbackWebSearch, clock: s.clock, body: body, model: model, stream: stream,
			inputTokens: inputTokens, enabled: enabled, headers: headers, tool: tool, query: query,
		}.serve(ctx, w)
	}
	credCtx := resolveAndInjectCredentials(ctx, providers.ProviderAnthropicGateway, model, headers)
	if CredentialsFromContext(credCtx) == nil {
		log.Info("Native web-search executor skipped: no gateway credential for this request")
		return fallbackWebSearchTurn{
			executor: s.fallbackWebSearch, clock: s.clock, body: body, model: model, stream: stream,
			inputTokens: inputTokens, enabled: enabled, headers: headers, tool: tool, query: query,
		}.serve(ctx, w)
	}

	start := s.clock.Now()
	result, err := s.webSearch.Search(credCtx, query)
	if err != nil {
		log.Warn("Native web-search execution failed; falling back to normal routing",
			"err", err, "latency_ms", s.clock.Now().Sub(start).Milliseconds())
		return false
	}

	msg := websearch.SynthesizeMessage(
		"msg_router_websearch_"+fmt.Sprintf("%x", s.clock.Now().UnixNano()),
		model, tool.Name, query, result, inputTokens,
	)
	if err := writeWebSearchMessage(w, msg, stream); err != nil {
		log.Warn("Failed writing synthesized web-search response", "err", err)
		return true
	}
	log.Info("Served native web-search turn on the tenant's own provider",
		"tool_type", tool.Type,
		"results", len(result.Results),
		"latency_ms", s.clock.Now().Sub(start).Milliseconds(),
	)
	return true
}

type fallbackWebSearchTurn struct {
	executor    websearch.Executor
	clock       auth.Clock
	body        []byte
	model       string
	stream      bool
	inputTokens int
	enabled     map[string]struct{}
	headers     http.Header
	tool        websearch.ServerTool
	query       websearch.Query
}

func (t fallbackWebSearchTurn) serve(ctx context.Context, w http.ResponseWriter) bool {
	if t.executor == nil {
		return false
	}
	log := observability.FromContext(ctx)
	if websearch.HasDomainFilter(t.body) {
		log.Info("Fallback web-search executor skipped: domain-filtered search left on normal routing",
			"tool_type", t.tool.Type)
		return false
	}
	if codexWebSearchMayServe(ctx, t.body, t.enabled, t.headers) {
		log.Info("Fallback web-search executor skipped: turn left for the Codex web-search bridge",
			"tool_type", t.tool.Type)
		return false
	}

	start := t.clock.Now()
	result, err := t.executor.Search(ctx, t.query)
	if err != nil {
		log.Warn("Fallback web-search execution failed; falling back to normal routing",
			"err", err, "latency_ms", t.clock.Now().Sub(start).Milliseconds())
		return false
	}

	msg := websearch.SynthesizeMessage(
		"msg_router_websearch_"+fmt.Sprintf("%x", t.clock.Now().UnixNano()),
		t.model, t.tool.Name, t.query, result, t.inputTokens,
	)
	if err := writeWebSearchMessage(w, msg, t.stream); err != nil {
		log.Warn("Failed writing synthesized fallback web-search response", "err", err)
		return true
	}
	log.Info("Served web-search turn on the fallback search executor",
		"tool_type", t.tool.Type,
		"results", len(result.Results),
		"latency_ms", t.clock.Now().Sub(start).Milliseconds(),
	)
	return true
}

func codexWebSearchMayServe(ctx context.Context, body []byte, enabled map[string]struct{}, headers http.Header) bool {
	if !websearch.IsClaudeCodeWebSearchHelper(body) || subscriptionAPIOnly(ctx) ||
		subscriptionRoutingDisabledForRequest(ctx) || subscriptionFundingOutOfPlayForRequest(ctx) {
		return false
	}
	if _, ok := enabled[providers.ProviderOpenAI]; !ok {
		return false
	}
	if managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) || codexSubscriptionFromContext(ctx) != nil {
		return true
	}
	creds := ExtractClientCredentials(providers.ProviderOpenAI, headers)
	return creds != nil && creds.OAuth
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
