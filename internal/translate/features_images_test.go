package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestHasImages_OpenAI(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantCount int
	}{
		{
			name:      "image_url part",
			body:      `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]}]}`,
			wantCount: 1,
		},
		{
			name:      "text parts only",
			body:      `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			wantCount: 0,
		},
		{
			name:      "string content",
			body:      `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`,
			wantCount: 0,
		},
		{
			name:      "image earlier in history",
			body:      `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":"and now"}]}`,
			wantCount: 1,
		},
		{
			name:      "multiple images across messages",
			body:      `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"one"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"two"}},{"type":"image_url","image_url":{"url":"three"}}]}]}`,
			wantCount: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseOpenAI([]byte(tc.body))
			require.NoError(t, err)
			features := env.RoutingFeatures(false)
			assert.Equal(t, tc.wantCount > 0, env.HasImages())
			assert.Equal(t, tc.wantCount > 0, features.HasImages)
			assert.Equal(t, tc.wantCount, env.ImageCount())
			assert.Equal(t, tc.wantCount, features.ImageCount)
		})
	}
}

func TestHasImages_Anthropic(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantCount int
	}{
		{
			name:      "image block",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}`,
			wantCount: 1,
		},
		{
			name:      "text block only",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			wantCount: 0,
		},
		{
			name:      "string content",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`,
			wantCount: 0,
		},
		{
			// An agent handing back a screenshot nests the image inside the
			// tool_result rather than at the top level. Every image capability
			// gate keys off this one signal, so missing this shape lets the turn
			// dispatch to a text-only model.
			name:      "image nested in tool_result",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}]}`,
			wantCount: 1,
		},
		{
			name:      "tool_result with text only",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"file contents"}]}]}]}`,
			wantCount: 0,
		},
		{
			name:      "tool_result with string content",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`,
			wantCount: 0,
		},
		{
			name:      "multiple images including tool_result",
			body:      `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"one"}}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"two"}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"three"}}]}]}]}`,
			wantCount: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(tc.body))
			require.NoError(t, err)
			features := env.RoutingFeatures(false)
			assert.Equal(t, tc.wantCount > 0, env.HasImages())
			assert.Equal(t, tc.wantCount > 0, features.HasImages)
			assert.Equal(t, tc.wantCount, env.ImageCount())
			assert.Equal(t, tc.wantCount, features.ImageCount)
		})
	}
}

func TestHasImages_Gemini(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantCount int
	}{
		{
			name:      "inlineData part",
			body:      `{"contents":[{"role":"user","parts":[{"text":"what is this"},{"inlineData":{"mimeType":"image/png","data":"AAA"}}]}]}`,
			wantCount: 1,
		},
		{
			name:      "fileData part",
			body:      `{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"image/png","fileUri":"gs://x"}}]}]}`,
			wantCount: 1,
		},
		{
			name:      "snake_case inline_data",
			body:      `{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"AAA"}}]}]}`,
			wantCount: 1,
		},
		{
			name:      "snake_case file_data",
			body:      `{"contents":[{"role":"user","parts":[{"file_data":{"mime_type":"image/png","file_uri":"gs://x"}}]}]}`,
			wantCount: 1,
		},
		{
			name:      "text parts only",
			body:      `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			wantCount: 0,
		},
		{
			name:      "multiple image parts",
			body:      `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"one"}},{"file_data":{"mime_type":"image/png","file_uri":"gs://two"}},{"inline_data":{"mime_type":"image/png","data":"three"}}]}]}`,
			wantCount: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseGemini([]byte(tc.body))
			require.NoError(t, err)
			features := env.RoutingFeatures(false)
			assert.Equal(t, tc.wantCount > 0, env.HasImages())
			assert.Equal(t, tc.wantCount > 0, features.HasImages)
			assert.Equal(t, tc.wantCount, env.ImageCount())
			assert.Equal(t, tc.wantCount, features.ImageCount)
		})
	}
}

func TestImageCountAcrossConversationHistory(t *testing.T) {
	t.Run("openai multiple messages", func(t *testing.T) {
		env, err := translate.ParseOpenAI([]byte(`{"model":"gpt-5.5","messages":[
			{"role":"user","content":[{"type":"image_url","image_url":{"url":"one"}}]},
			{"role":"assistant","content":"seen"},
			{"role":"user","content":[{"type":"image_url","image_url":{"url":"two"}},{"type":"image_url","image_url":{"url":"three"}}]}
		]}`))
		require.NoError(t, err)
		assert.Equal(t, 3, env.ImageCount())
	})
	t.Run("anthropic multiple blocks including tool result", func(t *testing.T) {
		env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","messages":[
			{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"one"}}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"two"}},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"three"}}
			]}]}
		]}`))
		require.NoError(t, err)
		assert.Equal(t, 3, env.ImageCount())
	})
	t.Run("gemini multiple media parts", func(t *testing.T) {
		env, err := translate.ParseGemini([]byte(`{"contents":[
			{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"one"}}]},
			{"role":"model","parts":[{"fileData":{"mimeType":"image/png","fileUri":"gs://two"}},{"inline_data":{"mime_type":"image/png","data":"three"}}]}
		]}`))
		require.NoError(t, err)
		assert.Equal(t, 3, env.ImageCount())
	})
}
