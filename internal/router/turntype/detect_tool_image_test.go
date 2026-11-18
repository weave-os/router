package turntype_test

import (
	"testing"

	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tool result that returns an image continues the tool flow on every
// ingress; it must not be classified as a fresh user prompt.
func TestDetectFromEnvelope_ToolResultCarryingImage(t *testing.T) {
	tests := []struct {
		name  string
		parse func([]byte) (*translate.RequestEnvelope, error)
		body  string
	}{
		{
			name:  "anthropic tool_result with an image block",
			parse: translate.ParseAnthropic,
			body: `{"model":"claude-sonnet-4-5","messages":[
				{"role":"user","content":"check the layout"},
				{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Screenshot","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QQ=="}}
				]}]}
			]}`,
		},
		{
			name:  "chat tool message with image content",
			parse: translate.ParseOpenAI,
			body: `{"model":"gpt-4o","messages":[
				{"role":"user","content":"check the layout"},
				{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"view_image","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"call_1","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}]}
			]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := tc.parse([]byte(tc.body))
			require.NoError(t, err)
			feats := env.RoutingFeatures()
			assert.Equal(t, "check the layout", feats.OnlyUserMessageText)
			assert.Equal(t, turntype.ToolResult, turntype.DetectFromEnvelope(env, feats, ""))
		})
	}
}
