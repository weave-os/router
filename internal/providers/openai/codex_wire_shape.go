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

func codexBackendBody(body []byte) []byte {
	body = stripCodexUnsupportedParams(body)
	body = listCodexInput(body)
	body = disableCodexStore(body)
	return demoteCodexSystemItems(body)
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

func listCodexInput(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if input.Type != gjson.String {
		return body
	}
	message := codexInputMessage{Type: "message", Role: "user", Content: []codexInputText{{Type: "input_text", Text: input.Str}}}
	out, err := sjson.SetBytes(body, "input", []codexInputMessage{message})
	if err != nil {
		return body
	}
	return out
}

func disableCodexStore(body []byte) []byte {
	if gjson.GetBytes(body, "store").Type == gjson.False {
		return body
	}
	out, err := sjson.SetBytes(body, "store", false)
	if err != nil {
		return body
	}
	return out
}

func demoteCodexSystemItems(body []byte) []byte {
	for i, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("role").Str != "system" {
			continue
		}
		out, err := sjson.SetBytes(body, fmt.Sprintf("input.%d.role", i), "developer")
		if err != nil {
			continue
		}
		body = out
	}
	return body
}
