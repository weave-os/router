package openai

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var codexUnsupportedParams = []string{
	"max_output_tokens", "temperature", "top_p", "metadata", "service_tier", "truncation",
}

type codexInputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexInputMessage struct {
	Type    string           `json:"type"`
	Role    string           `json:"role"`
	Content []codexInputText `json:"content"`
}

func codexDispatchBody(body []byte) (shaped []byte, collectForcedStream bool, err error) {
	shaped, err = codexBackendBody(body)
	if err != nil {
		return nil, false, fmt.Errorf("shape Codex body: %w", err)
	}
	if gjson.GetBytes(shaped, "stream").Bool() {
		return shaped, false, nil
	}
	streamed, err := sjson.SetBytes(shaped, "stream", true)
	if err != nil {
		return nil, false, fmt.Errorf("force Codex stream: %w", err)
	}
	return streamed, true, nil
}

func codexBackendBody(body []byte) ([]byte, error) {
	body = stripCodexUnsupportedParams(body)
	body, err := wrapCodexStringInput(body)
	if err != nil {
		return nil, fmt.Errorf("wrap Codex string input: %w", err)
	}
	if body, err = disableCodexStore(body); err != nil {
		return nil, fmt.Errorf("disable Codex store: %w", err)
	}
	if body, err = demoteCodexSystemItems(body); err != nil {
		return nil, fmt.Errorf("demote Codex system items: %w", err)
	}
	return body, nil
}

func stripCodexUnsupportedParams(body []byte) []byte {
	for _, key := range codexUnsupportedParams {
		if !gjson.GetBytes(body, key).Exists() {
			continue
		}
		out, err := sjson.DeleteBytes(body, key)
		if err != nil {
			continue
		}
		body = out
	}
	return body
}

func wrapCodexStringInput(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if input.Type != gjson.String {
		return body, nil
	}
	message := codexInputMessage{
		Type:    "message",
		Role:    "user",
		Content: []codexInputText{{Type: "input_text", Text: input.Str}},
	}
	return sjson.SetBytes(body, "input", []codexInputMessage{message})
}

func disableCodexStore(body []byte) ([]byte, error) {
	if gjson.GetBytes(body, "store").Type == gjson.False {
		return body, nil
	}
	return sjson.SetBytes(body, "store", false)
}

func demoteCodexSystemItems(body []byte) ([]byte, error) {
	for i, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("role").Str != "system" {
			continue
		}
		out, err := sjson.SetBytes(body, fmt.Sprintf("input.%d.role", i), "developer")
		if err != nil {
			return nil, err
		}
		body = out
	}
	return body, nil
}
