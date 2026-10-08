package translate_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

func TestPrepareOpenAIAliasesLongToolNamesReversibly(t *testing.T) {
	long := "mcp__claude_ai_Atlassian_Rovo_2__getJiraProjectIssueTypesMetadata"
	sibling := long[:64] + "Y"
	valid := strings.Repeat("v", 64)
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-5-5","max_tokens":1024,
	  "tools":[{"name":%[1]q,"input_schema":{"type":"object"}},{"name":%[2]q,"input_schema":{"type":"object"}},{"name":%[3]q,"input_schema":{"type":"object"}}],
	  "tool_choice":{"type":"tool","name":%[1]q},
	  "messages":[{"role":"user","content":"hi"},
	    {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":%[1]q,"input":{}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}]}`, long, sibling, valid))
	cases := map[string]struct {
		prepare  func(*translate.RequestEnvelope) (providers.PreparedRequest, error)
		declared string
		history  string
		choice   string
	}{
		"chat": {
			prepare: func(env *translate.RequestEnvelope) (providers.PreparedRequest, error) {
				return env.PrepareOpenAI(http.Header{}, translate.EmitOptions{TargetModel: "gpt-5.5"})
			},
			declared: "tools.#.function.name",
			history:  "messages.#.tool_calls.0.function.name|@flatten",
			choice:   "tool_choice.function.name",
		},
		"responses": {
			prepare: func(env *translate.RequestEnvelope) (providers.PreparedRequest, error) {
				return env.PrepareOpenAIResponses(http.Header{}, translate.EmitOptions{TargetModel: "gpt-5.5"})
			},
			declared: "tools.#.name",
			history:  `input.#(type=="function_call")#.name`,
			choice:   "tool_choice.name",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			prep, err := tc.prepare(env)
			require.NoError(t, err)
			declared := gjson.GetBytes(prep.Body, tc.declared).Array()
			require.Len(t, declared, 3)
			alias, siblingAlias := declared[0].String(), declared[1].String()
			require.NotEqual(t, alias, siblingAlias)
			for _, wire := range []string{alias, siblingAlias} {
				require.Regexp(t, `^[a-zA-Z0-9_-]{1,64}$`, wire)
			}
			require.Equal(t, valid, declared[2].String())
			require.Equal(t, alias, gjson.GetBytes(prep.Body, tc.history).Array()[0].String())
			require.Equal(t, alias, gjson.GetBytes(prep.Body, tc.choice).String())
			require.Equal(t, map[string]string{alias: long, siblingAlias: sibling}, prep.ResponseToolNames)
		})
	}
}

func TestOpenAIToolNameWriterRestoresClientNames(t *testing.T) {
	const alias, original = "short_alias", "the.original/tool name"
	cases := map[string]struct {
		contentType string
		body        string
	}{
		"chat stream":                            {"text/event-stream", fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":%q,\"arguments\":\"\"}}]}}]}\n\ndata: [DONE]\n\n", alias)},
		"chat stream without trailing separator": {"text/event-stream", fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":%q,\"arguments\":\"\"}}]}}]}", alias)},
		"chat json":                              {"application/json", fmt.Sprintf(`{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":%q,"arguments":"{}"}}]}}]}`, alias)},
		"responses stream":                       {"text/event-stream", fmt.Sprintf("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":%[1]q}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"function_call\",\"name\":%[1]q}]}}\n\n", alias)},
		"responses json":                         {"application/json", fmt.Sprintf(`{"output":[{"type":"function_call","call_id":"c1","name":%q,"arguments":"{}"}]}`, alias)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sink := httptest.NewRecorder()
			writer := translate.NewOpenAIToolNameWriter(sink, map[string]string{alias: original})
			writer.Header().Set("Content-Type", tc.contentType)
			writer.WriteHeader(http.StatusOK)
			_, err := writer.Write([]byte(tc.body))
			require.NoError(t, err)
			require.NoError(t, writer.Finalize())
			require.NotContains(t, sink.Body.String(), alias)
			require.Contains(t, sink.Body.String(), fmt.Sprintf("%q", original))
		})
	}
}
