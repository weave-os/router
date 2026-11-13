package translate

import (
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	clientCompactionTestOpus  = "claude-opus-5"
	clientCompactionTestSol   = "gpt-5.6-sol"
	clientCompactionTestTools = `"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]`
)

func TestAppendClientCompactionInstruction(t *testing.T) {
	for _, tt := range []struct{ name, content, tail string }{
		{"string prompt", `"summarize"`, ""},
		{"tool results beside instruction", `[{"type":"tool_result","tool_use_id":"read-1","content":"saved evidence"},{"type":"text","text":"summarize"}]`, ""},
		{"trailing system compaction instruction", `"summarize"`, `,{"role":"system","content":"Your task is to create a detailed summary. Do not call any tools."}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"system":"unchanged rules","max_tokens":64000,"messages":[{"role":"user","content":"original goal"},{"role":"assistant","content":"progress"},{"role":"user","content":` + tt.content + `}` + tt.tail + `]}`)
			env, err := ParseAnthropic(body)
			require.NoError(t, err)
			err = env.AppendClientCompactionInstruction("Resume with a bounded read.")
			require.NoError(t, err)
			assert.Contains(t, env.LastUserMessage().Text, "Resume with a bounded read.")
			assert.JSONEq(t, gjson.GetBytes(body, "messages.2.content").Raw, gjson.GetBytes(env.body, "messages.2.content").Raw)
			if tt.tail != "" {
				assert.JSONEq(t, gjson.GetBytes(body, "messages.3").Raw, gjson.GetBytes(env.body, "messages.3").Raw)
			}
			assert.Equal(t, "original goal", env.FirstUserMessageText())
			assert.Equal(t, "unchanged rules", gjson.GetBytes(env.body, "system").String())
			assert.Equal(t, int64(64000), gjson.GetBytes(env.body, "max_tokens").Int(), "do not truncate a summary's reasoning")
			if tt.name == "tool results beside instruction" {
				assert.Equal(t, "saved evidence", gjson.GetBytes(env.body, "messages.2.content.0.content").String())
			}
		})
	}
}

func TestDisableParallelToolUsePreservesHistoryAndChoice(t *testing.T) {
	for _, tt := range []struct {
		name, choice string
		changed      bool
	}{
		{"default", "", true},
		{"auto", `,"tool_choice":{"type":"auto"}`, true},
		{"named", `,"tool_choice":{"type":"tool","name":"Read"}`, true},
		{"none", `,"tool_choice":{"type":"none"}`, false},
		{"invalid preserved for upstream", `,"tool_choice":"invalid"`, false},
		{"already serial", `,"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"system":"rules",` + clientCompactionTestTools + tt.choice + `,"messages":[{"role":"user","content":"continue"}]}`)
			env, err := ParseAnthropic(body)
			require.NoError(t, err)
			changed, err := env.DisableParallelToolUse()
			require.NoError(t, err)
			assert.Equal(t, tt.changed, changed)
			assert.Equal(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(env.body, "messages").Raw)
			assert.Equal(t, "rules", gjson.GetBytes(env.body, "system").String())
			if tt.changed {
				assert.True(t, gjson.GetBytes(env.body, "tool_choice.disable_parallel_tool_use").Bool())
				if tt.name == "named" {
					assert.Equal(t, "Read", gjson.GetBytes(env.body, "tool_choice.name").String())
				}
			} else {
				assert.Equal(t, string(body), string(env.body))
			}
		})
	}
}

func TestRecoveryParallelLimitSurvivesTargetPreparation(t *testing.T) {
	env, err := ParseAnthropic([]byte(`{"model":"` + clientCompactionTestOpus + `",` + clientCompactionTestTools + `,"messages":[{"role":"user","content":"continue"}]}`))
	require.NoError(t, err)
	_, err = env.DisableParallelToolUse()
	require.NoError(t, err)
	for _, tt := range []struct {
		name, model, provider, field string
		prepare                      func(http.Header, EmitOptions) (providers.PreparedRequest, error)
		want                         bool
	}{
		{"native", clientCompactionTestOpus, providers.ProviderAnthropic, "tool_choice.disable_parallel_tool_use", env.PrepareAnthropic, true},
		{"chat", clientCompactionTestSol, providers.ProviderOpenAI, "parallel_tool_calls", env.PrepareOpenAI, false},
		{"responses", clientCompactionTestSol, providers.ProviderOpenAI, "parallel_tool_calls", env.PrepareOpenAIResponses, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prepared, err := tt.prepare(nil, EmitOptions{TargetModel: tt.model, TargetProvider: tt.provider, Capabilities: router.Lookup(tt.model)})
			require.NoError(t, err)
			limit := gjson.GetBytes(prepared.Body, tt.field)
			require.True(t, limit.Exists(), string(prepared.Body))
			assert.Equal(t, tt.want, limit.Bool())
		})
	}
}

func TestHasContext1MBetaMultipleHeaderValues(t *testing.T) {
	headers := make(http.Header)
	headers.Add("Anthropic-Beta", "other")
	headers.Add("Anthropic-Beta", context1MBeta)
	assert.True(t, HasContext1MBeta(headers))
}
