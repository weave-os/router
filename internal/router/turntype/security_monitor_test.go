package turntype_test

import (
	"testing"

	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectFromEnvelope_SecurityMonitor(t *testing.T) {
	tests := []struct {
		name string
		body string
		want turntype.TurnType
	}{
		{
			// Models that reject disabled thinking get a 2048-token budget on
			// top of the 64-token verdict, so the prompt identifies the check.
			name: "security monitor stage 1 above the classifier size cap is classifier",
			body: `{"model":"claude-sonnet-5-5","max_tokens":2112,"stop_sequences":["</block>"],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.323; cc_entrypoint=cli;"},{"type":"text","text":"You are a security monitor for autonomous AI coding agents.\n\n## Context"}],"messages":[{"role":"user","content":"<system-reminder>context</system-reminder>"},{"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"{\"user\":\"run the deploy\"}\n"},{"type":"text","text":"</transcript>\n"}]}]}`,
			want: turntype.Classifier,
		},
		{
			name: "security monitor stage 2 thinking re-check is classifier",
			body: `{"model":"claude-sonnet-5-5","max_tokens":10240,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.323; cc_entrypoint=cli;"},{"type":"text","text":"You are a security monitor for autonomous AI coding agents.\n\n## Context"}],"messages":[{"role":"user","content":"<system-reminder>context</system-reminder>"},{"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"{\"user\":\"run the deploy\"}\n"},{"type":"text","text":"</transcript>\n"}]}]}`,
			want: turntype.Classifier,
		},
		{
			name: "security monitor with one message per transcript block is classifier",
			body: `{"model":"claude-opus-5-5","max_tokens":2112,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.323; cc_entrypoint=cli;"},{"type":"text","text":"You are a security monitor for autonomous AI coding agents.\n\n## Context"}],"messages":[{"role":"user","content":"<transcript>\n"},{"role":"user","content":"{\"user\":\"run the deploy\"}\n"},{"role":"user","content":"{\"Bash\":\"make deploy\"}\n"},{"role":"user","content":"</transcript>\n"}]}`,
			want: turntype.Classifier,
		},
		{
			name: "security monitor prompt on a turn with tools stays main_loop",
			body: `{"model":"claude-sonnet-5-5","max_tokens":10240,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.323; cc_entrypoint=cli;"},{"type":"text","text":"You are a security monitor for autonomous AI coding agents.\n\n## Context"}],"messages":[{"role":"user","content":"review this"}]}`,
			want: turntype.MainLoop,
		},
		{
			name: "security monitor phrase inside a longer system prompt stays main_loop",
			body: `{"model":"claude-sonnet-5-5","max_tokens":10240,"system":"You help users write prompts. Example: You are a security monitor for autonomous AI coding agents.","messages":[{"role":"user","content":"improve it"}]}`,
			want: turntype.MainLoop,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, turntype.DetectFromEnvelope(env, env.RoutingFeatures(false), ""))
		})
	}
}
