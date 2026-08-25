package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnthropicClientResponseTextJSON(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"✦ **Weave Router** → model\n\nanswer"},{"type":"tool_use","name":"Read"}]}`)
	assert.Equal(t, "answer\n[tool call] Read", AnthropicClientResponseText(body))
}

func TestAnthropicClientResponseTextSSE(t *testing.T) {
	body := []byte("data: {\"content_block\":{\"type\":\"text\",\"text\":\"hello\"}}\n\n" +
		"data: {\"delta\":{\"text\":\" world\"}}\n\n" +
		"data: {\"content_block\":{\"type\":\"tool_use\",\"name\":\"Bash\"}}\n\n" +
		"data: [DONE]\n")
	assert.Equal(t, "hello world[tool call] Bash", AnthropicClientResponseText(body))
}
