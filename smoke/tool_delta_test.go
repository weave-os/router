//go:build smoke

package smoke

import (
	"encoding/json"
	"testing"
)

const toolDeltaPinModel = "claude-opus-5"

// TestToolDeltaBlocks sends mid-conversation tool-change blocks on user and
// assistant messages, matching the client shapes that previously reached
// Anthropic with the blocks on a non-system role.
func TestToolDeltaBlocks(t *testing.T) {
	body := toolDeltaRequest(t)
	r := callModel(t, body, toolDeltaPinModel)
	requireOKMessage(t, r)
	assertServedByModel(t, r, toolDeltaPinModel, "anthropic")
}

func toolDeltaRequest(t *testing.T) []byte {
	t.Helper()
	base := newRequest("smoke-tool-delta").tokens(64).build(t)
	var request map[string]any
	if err := json.Unmarshal(base, &request); err != nil {
		t.Fatalf("unmarshal base request: %v", err)
	}
	request["messages"] = []any{
		map[string]any{"role": "user", "content": "Start the conversation."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{
				"type": "tool_addition",
				"tool": map[string]any{"type": "tool_reference", "name": "Read"},
			},
			map[string]any{"type": "text", "text": "Continue with the available tools."},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{
				"type": "tool_removal",
				"tool": map[string]any{"type": "tool_reference", "name": "Edit"},
			},
			map[string]any{"type": "text", "text": "I will continue."},
		}},
		map[string]any{"role": "user", "content": "Now reply with exactly: ok"},
	}
	out, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal tool delta request: %v", err)
	}
	return out
}
