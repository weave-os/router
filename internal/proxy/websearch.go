package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/websearch"
)

// serveNativeWebSearch answers a native web-search turn when no enabled
// provider can execute it, by running the search on the tenant's own gateway
// and synthesizing the server_tool_use / web_search_tool_result blocks.
// Cortex inference rejects web_search_20250305; agent:run on the same host
// accepts it. Returns true if served; falls through when no executor, a
// capable provider is in the pool, the turn is not a search sub-turn, or
// the search fails.
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
	if s.webSearch == nil {
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

	credCtx := resolveAndInjectCredentials(ctx, providers.ProviderAnthropicGateway, model, headers)
	if CredentialsFromContext(credCtx) == nil {
		log.Info("Native web-search executor skipped: no gateway credential for this request")
		return false
	}

	start := time.Now()
	result, err := s.webSearch.Search(credCtx, query)
	if err != nil {
		log.Warn("Native web-search execution failed; falling back to normal routing",
			"err", err, "latency_ms", time.Since(start).Milliseconds())
		return false
	}

	msg := websearch.SynthesizeMessage(
		"msg_router_websearch_"+fmt.Sprintf("%x", time.Now().UnixNano()),
		model, tool.Name, query, result, inputTokens,
	)
	if err := writeWebSearchMessage(w, msg, stream); err != nil {
		log.Warn("Failed writing synthesized web-search response", "err", err)
		return true
	}
	log.Info("Served native web-search turn on the tenant's own provider",
		"tool_type", tool.Type,
		"results", len(result.Results),
		"latency_ms", time.Since(start).Milliseconds(),
	)
	return true
}

// anyNativeServerToolProvider reports whether any enabled provider runs
// Anthropic server tools natively, in which case the turn stays on routing.
func anyNativeServerToolProvider(enabled map[string]struct{}) bool {
	for provider := range enabled {
		if providers.SupportsAnthropicServerTools(provider) {
			return true
		}
	}
	return false
}

// writeWebSearchMessage emits the synthesized message in the shape the client
// asked for.
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

// writeWebSearchSSE streams the synthesized message as Anthropic Messages SSE.
// server_tool_use input arrives as an input_json_delta, matching the real API —
// a client that reads tool input only from deltas otherwise sees an empty query.
func writeWebSearchSSE(w http.ResponseWriter, msg websearch.Message) error {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	bw := bufio.NewWriterSize(w, 8192)

	bw.WriteString(sseEvent("message_start", mustMarshalJSON(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msg.ID, "type": "message", "role": "assistant",
			"content": []any{}, "model": msg.Model,
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": msg.Usage.InputTokens, "output_tokens": 0},
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
