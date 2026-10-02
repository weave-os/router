package main

import (
	"encoding/json"
	"net/http"
	"reflect"
)

const startupFixtureKey = "smoke-fixture-key-unused-outside-replay"

// Startup fixtures are authored responses, never provider recordings. They only
// match the bounded boot prompt with the isolated runner's fake credentials.
func startupFixture(host string, req *http.Request, body []byte) *cassette {
	if req.Method != http.MethodPost || req.URL.RawQuery != "" || len(body) > 8<<10 {
		return nil
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return nil
	}
	model, ok := request["model"].(string)
	if !ok || model == "" {
		return nil
	}
	expected := map[string]any{"model": model, "stream": false}
	var response map[string]any
	switch {
	case host == "api.anthropic.com" && req.URL.Path == "/v1/messages" && req.Header.Get("X-Api-Key") == startupFixtureKey:
		expected["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "text", "text": "Reply with OK.", "cache_control": map[string]any{"type": "ephemeral"},
		}}}}
		expected["max_tokens"] = request["max_tokens"]
		if tokens, ok := request["max_tokens"].(float64); !ok || (tokens != 32 && tokens != 1024) {
			return nil
		}
		if request["max_tokens"] == float64(1024) {
			expected["thinking"] = map[string]any{"type": "adaptive"}
			expected["output_config"] = map[string]any{"effort": "low"}
		}
		response = map[string]any{
			"id": "msg_smoke_startup", "type": "message", "role": "assistant", "model": model,
			"content":     []any{map[string]any{"type": "text", "text": "OK"}},
			"stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		}
	case host == "api.openai.com" && req.URL.Path == "/v1/chat/completions" && req.Header.Get("Authorization") == "Bearer "+startupFixtureKey:
		expected["messages"] = []any{map[string]any{"role": "user", "content": "Reply with OK."}}
		expected["max_tokens"] = float64(32)
		response = map[string]any{
			"id": "chatcmpl_smoke_startup", "object": "chat.completion", "model": model,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		}
	case host == "api.openai.com" && req.URL.Path == "/v1/responses" && req.Header.Get("Authorization") == "Bearer "+startupFixtureKey:
		expected["input"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with OK."}}}}
		expected["store"] = false
		expected["max_output_tokens"] = float64(1024)
		expected["reasoning"] = map[string]any{"effort": "low", "summary": "detailed"}
		response = map[string]any{
			"id": "resp_smoke_startup", "object": "response", "status": "completed", "model": model,
			"output": []any{map[string]any{"id": "msg_smoke_startup", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "OK", "annotations": []any{}}}}},
			"usage":  map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		}
	default:
		return nil
	}
	if !reflect.DeepEqual(request, expected) {
		return nil
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil
	}
	return &cassette{
		Method: req.Method, Path: req.URL.Path, StatusCode: http.StatusOK,
		Headers: map[string]string{"Content-Type": "application/json", "X-Smoke-Fixture": "router-startup"}, Body: encoded,
	}
}
