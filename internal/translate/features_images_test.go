package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestHasImages_OpenAI(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "image_url part",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]}]}`,
			want: true,
		},
		{
			name: "text parts only",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			want: false,
		},
		{
			name: "string content",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`,
			want: false,
		},
		{
			name: "image earlier in history",
			body: `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":"and now"}]}`,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseOpenAI([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.HasImages())
			assert.Equal(t, tc.want, env.RoutingFeatures(false).HasImages)
			if tc.want {
				assert.GreaterOrEqual(t, env.ImageCount(), 1)
			} else {
				assert.Zero(t, env.ImageCount())
			}
			assert.Equal(t, env.ImageCount(), env.RoutingFeatures(false).ImageCount)
		})
	}
}

func TestHasImages_Anthropic(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "image block",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}`,
			want: true,
		},
		{
			name: "text block only",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			want: false,
		},
		{
			name: "string content",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`,
			want: false,
		},
		{
			// An agent handing back a screenshot nests the image inside the
			// tool_result rather than at the top level. Every image capability
			// gate keys off this one signal, so missing this shape lets the turn
			// dispatch to a text-only model.
			name: "image nested in tool_result",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}]}`,
			want: true,
		},
		{
			name: "tool_result with text only",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"file contents"}]}]}]}`,
			want: false,
		},
		{
			name: "tool_result with string content",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.HasImages())
			assert.Equal(t, tc.want, env.RoutingFeatures(false).HasImages)
			if tc.want {
				assert.GreaterOrEqual(t, env.ImageCount(), 1)
			} else {
				assert.Zero(t, env.ImageCount())
			}
			assert.Equal(t, env.ImageCount(), env.RoutingFeatures(false).ImageCount)
		})
	}
}

func TestHasImages_Gemini(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "inlineData part",
			body: `{"contents":[{"role":"user","parts":[{"text":"what is this"},{"inlineData":{"mimeType":"image/png","data":"AAA"}}]}]}`,
			want: true,
		},
		{
			name: "fileData part",
			body: `{"contents":[{"role":"user","parts":[{"fileData":{"mimeType":"image/png","fileUri":"gs://x"}}]}]}`,
			want: true,
		},
		{
			name: "snake_case inline_data",
			body: `{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"AAA"}}]}]}`,
			want: true,
		},
		{
			name: "snake_case file_data",
			body: `{"contents":[{"role":"user","parts":[{"file_data":{"mime_type":"image/png","file_uri":"gs://x"}}]}]}`,
			want: true,
		},
		{
			name: "text parts only",
			body: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseGemini([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.HasImages())
			assert.Equal(t, tc.want, env.RoutingFeatures(false).HasImages)
			if tc.want {
				assert.GreaterOrEqual(t, env.ImageCount(), 1)
			} else {
				assert.Zero(t, env.ImageCount())
			}
			assert.Equal(t, env.ImageCount(), env.RoutingFeatures(false).ImageCount)
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
