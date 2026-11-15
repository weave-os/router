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

func TestAnthropicErrorAsOpenAILeavesAnOpenAIBodyUnchanged(t *testing.T) {
	body := []byte(`{"error":{"message":"slow down","type":"requests","param":null,"code":"rate_limit_exceeded"}}`)
	assert.Equal(t, string(body), string(translate.AnthropicErrorAsOpenAI(body)))
}
