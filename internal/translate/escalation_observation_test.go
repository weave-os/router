package translate_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/translate"
)

func TestEscalationObservationRejectsMalformedNamedTools(t *testing.T) {
	for _, tool := range []string{`null`, `{}`, `{"name":null}`, `{"name":7}`, `{"name":""}`, `{"name":"  "}`} {
		fixtures := []struct {
			name  string
			body  string
			parse func([]byte) (*translate.RequestEnvelope, error)
		}{
			{"chat", fmt.Sprintf(`{"messages":[{"role":"assistant","tool_calls":[{"function":%s}]}]}`, tool), translate.ParseOpenAI},
			{"chat legacy", fmt.Sprintf(`{"messages":[{"role":"assistant","function_call":%s}]}`, tool), translate.ParseOpenAI},
			{"gemini call", fmt.Sprintf(`{"contents":[{"role":"model","parts":[{"functionCall":%s}]}]}`, tool), translate.ParseGemini},
			{"gemini result", fmt.Sprintf(`{"contents":[{"role":"user","parts":[{"functionResponse":%s}]}]}`, tool), translate.ParseGemini},
		}
		for _, fixture := range fixtures {
			t.Run(fixture.name+tool, func(t *testing.T) {
				envelope, err := fixture.parse([]byte(fixture.body))
				require.NoError(t, err)
				_, err = envelope.EscalationObservation()
				require.Error(t, err)
			})
		}
	}
	for _, name := range []string{`null`, `7`, `""`, `"  "`} {
		t.Run("anthropic"+name, func(t *testing.T) {
			envelope, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":%s,"input":{}}]}]}`, name)))
			require.NoError(t, err)
			_, err = envelope.EscalationObservation()
			require.Error(t, err)
		})
		for _, kind := range []string{"function_call", "custom_tool_call"} {
			t.Run(kind+name, func(t *testing.T) {
				_, err := translate.ParseResponsesEscalationObservation([]byte(fmt.Sprintf(`{"input":[{"type":%q,"name":%s,"arguments":"{}","input":""}]}`, kind, name)))
				require.Error(t, err)
			})
		}
	}
}

func TestEscalationObservationPreservesToolIdentityAcrossProtocols(t *testing.T) {
	fixtures := []struct {
		name  string
		body  string
		parse func([]byte) (*translate.RequestEnvelope, error)
	}{
		{"anthropic", `{"messages":[{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"call-1","name":"run","input":{"command":"go test"}},{"type":"text","text":"waiting"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","is_error":true,"content":"failed"}]}]}`, translate.ParseAnthropic},
		{"chat", `{"messages":[{"role":"assistant","content":"checking","tool_calls":[{"id":"call-1","type":"function","function":{"name":"run","arguments":"{\"command\":\"go test\"}"}}]},{"role":"tool","tool_call_id":"call-1","is_error":true,"content":"failed"}]}`, translate.ParseOpenAI},
		{"gemini", `{"contents":[{"role":"model","parts":[{"text":"checking"},{"functionCall":{"id":"call-1","name":"run","args":{"command":"go test"}}},{"text":"waiting"}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"run","response":{"error":"failed"}}}]}]}`, translate.ParseGemini},
		{"responses", `{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"checking"}]},{"type":"function_call","call_id":"call-1","name":"run","arguments":"{\"command\":\"go test\"}"},{"type":"function_call_output","call_id":"call-1","is_error":true,"output":"failed"}]}`, translate.ParseOpenAI},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			envelope, err := fixture.parse([]byte(fixture.body))
			require.NoError(t, err)
			observation, err := envelope.EscalationObservation()
			require.NoError(t, err)
			assert.True(t, observation.HistoryComplete)
			assert.Equal(t, translate.EscalationTurnToolResult, observation.TurnType)
			var calls, outputs []translate.EscalationBlock
			for _, message := range observation.Messages {
				for _, block := range message.Blocks {
					switch block.Type {
					case translate.EscalationBlockToolCall:
						calls = append(calls, block)
					case translate.EscalationBlockToolResult:
						outputs = append(outputs, block)
					}
				}
			}
			require.Len(t, calls, 1)
			require.Len(t, outputs, 1)
			assert.Equal(t, "call-1", calls[0].ID)
			assert.Equal(t, "run", calls[0].Name)
			assert.JSONEq(t, `{"command":"go test"}`, calls[0].ArgumentsJSON)
			assert.Equal(t, "call-1", outputs[0].CallID)
			require.NotNil(t, outputs[0].IsError)
			assert.True(t, *outputs[0].IsError)
			assert.True(t, json.Valid([]byte(outputs[0].ContentJSON)))
		})
	}
}

func TestEscalationObservationPreservesInterleavingAndMissingError(t *testing.T) {
	envelope, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"before"},{"type":"tool_use","id":"a","name":"run","input":{"z":1,"a":2}},{"type":"text","text":"after"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"done"}]},{"type":"text","text":"next"}]}]}`))
	require.NoError(t, err)
	observation, err := envelope.EscalationObservation()
	require.NoError(t, err)
	require.Len(t, observation.Messages, 2)
	assert.Equal(t, []translate.EscalationBlockType{translate.EscalationBlockText, translate.EscalationBlockToolCall, translate.EscalationBlockText}, []translate.EscalationBlockType{observation.Messages[0].Blocks[0].Type, observation.Messages[0].Blocks[1].Type, observation.Messages[0].Blocks[2].Type})
	assert.Equal(t, `{"z":1,"a":2}`, observation.Messages[0].Blocks[1].ArgumentsJSON)
	assert.Equal(t, `[{"type":"text","text":"done"}]`, observation.Messages[1].Blocks[0].ContentJSON)
	assert.Nil(t, observation.Messages[1].Blocks[0].IsError)
	assert.Equal(t, "next", observation.Messages[1].Blocks[1].Text)
}

func TestResponsesEscalationRetainsCustomToolsAndContinuation(t *testing.T) {
	observation, err := translate.ParseResponsesEscalationObservation([]byte(`{"previous_response_id":"resp-1","input":[{"type":"item_reference","id":"item-1"},{"type":"custom_tool_call","call_id":"custom-1","name":"apply_patch","namespace":"tools","input":"*** patch\n"},{"type":"custom_tool_call_output","call_id":"custom-1","output":[{"type":"input_text","text":"done"}]}]}`))
	require.NoError(t, err)
	assert.False(t, observation.HistoryComplete)
	assert.Equal(t, "resp-1", observation.ContinuationID)
	assert.Equal(t, []string{"item-1"}, observation.ItemReferenceIDs)
	require.Len(t, observation.Messages, 2)
	call := observation.Messages[0].Blocks[0]
	assert.Equal(t, "custom-1", call.ID)
	assert.Equal(t, "tools", call.Namespace)
	assert.Equal(t, `"*** patch\n"`, call.ArgumentsJSON)
	assert.JSONEq(t, `[{"type":"input_text","text":"done"}]`, observation.Messages[1].Blocks[0].ContentJSON)
}

func TestGeminiEscalationDoesNotInventCallIDs(t *testing.T) {
	envelope, err := translate.ParseGemini([]byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"run","args":{}}},{"functionCall":{"name":"run","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"run","response":{"result":"done"}}}]}]}`))
	require.NoError(t, err)
	observation, err := envelope.EscalationObservation()
	require.NoError(t, err)
	assert.Empty(t, observation.Messages[0].Blocks[0].ID)
	assert.Empty(t, observation.Messages[0].Blocks[1].ID)
	assert.Empty(t, observation.Messages[1].Blocks[0].CallID)
	assert.Equal(t, "run", observation.Messages[1].Blocks[0].Name)
	assert.Nil(t, observation.Messages[1].Blocks[0].IsError)
}

func TestEscalationObservationExcludesOpaqueReasoningAndMedia(t *testing.T) {
	observation, err := translate.ParseResponsesEscalationObservation([]byte(`{"input":[{"type":"reasoning","encrypted_content":"secret"},{"type":"message","role":"developer","content":"instructions"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"secret-image"},{"type":"input_text","text":"check"}]}]}`))
	require.NoError(t, err)
	require.Len(t, observation.Messages, 2)
	assert.Equal(t, translate.EscalationRoleSystem, observation.Messages[0].Role)
	encoded, err := json.Marshal(observation)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "secret")
	assert.Contains(t, string(encoded), "check")
}

func TestEscalationObservationRejectsUnsupportedEvidence(t *testing.T) {
	for _, body := range []string{`{"input":[{"type":"future_tool"}]}`, `{"input":[{"type":"item_reference"}]}`, `{"input":{}}`, `{"messages":[]}`} {
		_, err := translate.ParseResponsesEscalationObservation([]byte(body))
		assert.Error(t, err)
	}
	envelope, err := translate.ParseAnthropic([]byte(`{"messages":[{"role":"user","content":[{"type":"future_block"}]}]}`))
	require.NoError(t, err)
	_, err = envelope.EscalationObservation()
	assert.Error(t, err)
}
