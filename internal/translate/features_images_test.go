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
			body: `{"model":"Weave","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]}]}`,
			want: true,
		},
		{
			name: "text parts only",
			body: `{"model":"Weave","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			want: false,
		},
		{
			name: "string content",
			body: `{"model":"Weave","messages":[{"role":"user","content":"hello"}]}`,
			want: false,
		},
		{
			name: "image earlier in history",
			body: `{"model":"Weave","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":"and now"}]}`,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseOpenAI([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.HasImages())
			assert.Equal(t, tc.want, env.RoutingFeatures(false).HasImages)
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
			body: `{"model":"Weave","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}`,
			want: true,
		},
		{
			name: "text block only",
			body: `{"model":"Weave","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`,
			want: false,
		},
		{
			name: "string content",
			body: `{"model":"Weave","messages":[{"role":"user","content":"hello"}]}`,
			want: false,
		},
		{
			// An agent handing back a screenshot nests the image inside the
			// tool_result rather than at the top level. Every image capability
			// gate keys off this one signal, so missing this shape lets the turn
			// dispatch to a text-only model.
			name: "image nested in tool_result",
			body: `{"model":"Weave","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}]}`,
			want: true,
		},
		{
			name: "tool_result with text only",
			body: `{"model":"Weave","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"file contents"}]}]}]}`,
			want: false,
		},
		{
			name: "tool_result with string content",
			body: `{"model":"Weave","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.HasImages())
			assert.Equal(t, tc.want, env.RoutingFeatures(false).HasImages)
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
		})
	}
}
