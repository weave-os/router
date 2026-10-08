package translate_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/translate"
)

func TestAnthropicToolReferencesFollowDeclarationAliases(t *testing.T) {
	for _, length := range []int{128, 129} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			name := strings.Repeat("x", length)
			body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-5-5","max_tokens":1024,"tools":[{"name":%q,"defer_loading":true,"input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"search","content":[{"type":"tool_reference","name":%q},{"type":"text","text":%q}]}]}]}`, name, name, name))
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			prep, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5-5"})
			require.NoError(t, err)
			declared := gjson.GetBytes(prep.Body, "tools.0.name").String()
			require.Equal(t, declared, gjson.GetBytes(prep.Body, "messages.0.content.0.content.0.name").String())
			require.Equal(t, name, gjson.GetBytes(prep.Body, "messages.0.content.0.content.1.text").String())
			require.True(t, gjson.GetBytes(prep.Body, "tools.0.defer_loading").Bool())
			if length == 128 {
				require.Equal(t, name, declared)
			} else {
				require.NotEqual(t, name, declared)
			}
		})
	}
}

func TestAnthropicToolChangeReferencesFollowCollisionAlias(t *testing.T) {
	name := strings.Repeat("a", 129)
	collision := "invalid_tool_d96debf1bdcbc896e6c134ea76e8141f40d78536"
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-5-5","max_tokens":1024,"tools":[{"name":%q,"input_schema":{"type":"object"}},{"name":%q,"input_schema":{"type":"object"}}],"system":[{"type":"tool_addition","tool":{"type":"tool_reference","name":%q}}],"messages":[{"role":"user","content":"hello"},{"role":"system","content":[{"type":"tool_removal","tool":{"type":"tool_reference","name":%q}}]},{"role":"user","content":"continue"}]}`, collision, name, name, name))
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prep, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5-5"})
	require.NoError(t, err)
	alias := gjson.GetBytes(prep.Body, "tools.1.name").String()
	require.NotEqual(t, collision, alias)
	require.Equal(t, collision, gjson.GetBytes(prep.Body, "tools.0.name").String())
	require.Equal(t, alias, gjson.GetBytes(prep.Body, "system.0.tool.name").String())
	require.Equal(t, alias, gjson.GetBytes(prep.Body, "messages.1.content.0.tool.name").String())
	require.Equal(t, name, prep.ResponseToolNames[alias])
	_, collidingNameAliased := prep.ResponseToolNames[collision]
	require.False(t, collidingNameAliased)
}
