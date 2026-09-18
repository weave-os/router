package translate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WriteSyntheticAnthropicResponse renders a control reply without a provider request.
func WriteSyntheticAnthropicResponse(w http.ResponseWriter, env *RequestEnvelope, text string, inputTokens int) error {
	msgID := fmt.Sprintf("msg_router_cmd_%x", time.Now().UnixNano())
	if env.Stream() {
		return writeSyntheticAnthropicSSE(w, msgID, text, inputTokens)
	}
	return writeSyntheticAnthropicJSON(w, msgID, text, inputTokens)
}

func writeSyntheticAnthropicJSON(w http.ResponseWriter, msgID, text string, inputTokens int) error {
	resp := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         "weave-router",
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": len(text) / 4,
		},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal synthetic response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, writeErr := w.Write(body)
	return writeErr
}

func writeSyntheticAnthropicSSE(w http.ResponseWriter, msgID, text string, inputTokens int) error {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	bw := bufio.NewWriterSize(w, 4096)

	outTokens := len(text) / 4

	events := []string{
		commandSSEEvent("message_start", marshalCommandJSON(map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": msgID, "type": "message", "role": "assistant",
				"content": []any{}, "model": "weave-router",
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
			},
		})),
		commandSSEEvent("content_block_start", marshalCommandJSON(map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""},
		})),
		commandSSEEvent("ping", `{"type":"ping"}`),
		commandSSEEvent("content_block_delta", marshalCommandJSON(map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text},
		})),
		commandSSEEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
		commandSSEEvent("message_delta", marshalCommandJSON(map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": outTokens},
		})),
		commandSSEEvent("message_stop", `{"type":"message_stop"}`),
	}

	for _, ev := range events {
		bw.WriteString(ev)
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// WriteSyntheticOpenAIResponse writes a minimal OpenAI Chat Completions
// response without hitting an upstream, handling both streaming and
// non-streaming shapes.
func WriteSyntheticOpenAIResponse(w http.ResponseWriter, env *RequestEnvelope, text string, inputTokens int) error {
	respID := fmt.Sprintf("chatcmpl_router_cmd_%x", time.Now().UnixNano())
	if env.Stream() {
		return writeSyntheticOpenAISSE(w, respID, text, inputTokens)
	}
	return writeSyntheticOpenAIJSON(w, respID, text, inputTokens)
}

func writeSyntheticOpenAIJSON(w http.ResponseWriter, respID, text string, inputTokens int) error {
	outTokens := len(text) / 4
	resp := map[string]any{
		"id":      respID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "weave-router",
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outTokens,
			"total_tokens":      inputTokens + outTokens,
		},
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal synthetic openai response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, writeErr := w.Write(body)
	return writeErr
}

func writeSyntheticOpenAISSE(w http.ResponseWriter, respID, text string, inputTokens int) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	bw := bufio.NewWriterSize(w, 4096)
	created := time.Now().Unix()
	outTokens := len(text) / 4
	chunkStart := marshalCommandJSON(map[string]any{
		"id":      respID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   "weave-router",
		"choices": []any{
			map[string]any{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": nil,
			},
		},
	})
	chunkStop := marshalCommandJSON(map[string]any{
		"id":      respID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   "weave-router",
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outTokens,
			"total_tokens":      inputTokens + outTokens,
		},
	})
	events := []string{
		commandOpenAISSEData(chunkStart),
		commandOpenAISSEData(chunkStop),
		commandOpenAISSEData("[DONE]"),
	}
	for _, ev := range events {
		bw.WriteString(ev)
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func commandSSEEvent(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

func commandOpenAISSEData(data string) string {
	return "data: " + data + "\n\n"
}

func marshalCommandJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
