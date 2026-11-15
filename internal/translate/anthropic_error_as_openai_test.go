package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/translate"
)

func TestAnthropicErrorAsOpenAIRewrapsAnAnthropicEnvelope(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages: field required"}}`)
	assert.JSONEq(t,
		`{"error":{"message":"messages: field required","type":"invalid_request_error","param":null,"code":null}}`,
		string(translate.AnthropicErrorAsOpenAI(body)))
}

func TestAnthropicErrorAsOpenAILeavesOpenAIBodiesUnchanged(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"slow down","type":"requests","param":null,"code":"rate_limit_exceeded"}}`,
		`{"type":"error","sequence_number":3,"error":{"type":"invalid_request_error","code":"invalid_prompt","message":"bad","param":null}}`,
	} {
		assert.Equal(t, body, string(translate.AnthropicErrorAsOpenAI([]byte(body))))
	}
}
