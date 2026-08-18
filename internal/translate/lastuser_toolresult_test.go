package translate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/translate"
)

func TestLastUserToolResultErrorText(t *testing.T) {
	t.Run("non-anthropic format returns empty string", func(t *testing.T) {
		// OpenAI: tool-failure is encoded differently and is not a wire-level
		// is_error marker we can read, so the accessor returns "".
		body := []byte(`{"model":"gpt-4o","messages":[
			{"role":"user","content":"run grep"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"t1","content":"InputValidationError: schema mismatch"}
		]}`)
		env, err := translate.ParseOpenAI(body)
		require.NoError(t, err)
		assert.Equal(t, "", env.LastUserToolResultErrorText(4096))
	})

	t.Run("array-form errored tool_result text is concatenated", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-5","tools":[],"messages":[
			{"role":"user","content":"run"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"EnterPlanMode","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[
					{"type":"text","text":"InputValidationError: needs deferred schema for EnterPlanMode"}
				]},
				{"type":"text","text":"trailing user note ignored"}
			]}
		]}`)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		got := env.LastUserToolResultErrorText(8192)
		assert.Contains(t, got, "InputValidationError")
		assert.Contains(t, got, "EnterPlanMode")
	})

	t.Run("plain-string errored tool_result text is returned", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-7","tools":[],"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"X","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"InputValidationError: bad param"}
			]}
		]}`)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		assert.Contains(t, env.LastUserToolResultErrorText(4096), "InputValidationError")
	})

	t.Run("non-errored block with same text is not surfaced", func(t *testing.T) {
		// Successful tool_result printing InputValidationError must not be
		// picked up — the harness gate's Recovery classifier depends on this
		// (its is_error gate is the load-bearing half).
		body := []byte(`{"model":"claude-sonnet-4-5","tools":[],"messages":[
			{"role":"user","content":"grep"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","content":"found InputValidationError in logs"}
			]}
		]}`)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		assert.Equal(t, "", env.LastUserToolResultErrorText(4096))
	})

	t.Run("max bytes truncates output", func(t *testing.T) {
		pad := "x"
		for i := 0; i < 50; i++ {
			pad += "x"
		}
		body := []byte(`{"model":"claude-sonnet-4-5","tools":[],"messages":[
			{"role":"user","content":"r"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"X","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"InputValidationError ` + pad + ` long tail"}
			]}
		]}`)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		got := env.LastUserToolResultErrorText(64)
		assert.LessOrEqual(t, len(got), 64)
	})

	t.Run("max bytes <= 0 returns empty", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-7","tools":[],"messages":[
			{"role":"user","content":"r"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"X","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"InputValidationError"}
			]}
		]}`)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		assert.Equal(t, "", env.LastUserToolResultErrorText(0))
	})

	t.Run("only the last user message is read", func(t *testing.T) {
		// Earlier errored tool_result must be ignored; the gate looks at
		// the LAST user-side input, which is the context the next turn has
		// to act on.
		body := []byte(`{"model":"claude-sonnet-4-5","tools":[],"messages":[
			{"role":"user","content":"first step"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"X","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"InputValidationError OLD harness failure"}
			]},
			{"role":"assistant","content":[{"type":"text","text":"retrying..."}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":"InputValidationError fresh recovery context with EnterPlanMode"}
			]}
		]}`)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		got := env.LastUserToolResultErrorText(4096)
		// Last user message wins; earlier "OLD" payload must be ignored.
		assert.NotContains(t, got, "OLD")
		// Last payload's recovery-style text is what the gate sees.
		assert.Contains(t, got, "fresh recovery context")
	})

	t.Run("no user messages yields empty", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-7","tools":[],"messages":[]}`)
		env, err := translate.ParseAnthropic(body)
		require.NoError(t, err)
		assert.Equal(t, "", env.LastUserToolResultErrorText(4096))
	})
}
