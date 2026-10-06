package translate

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesFreeformSchemaPreservesJSON(t *testing.T) {
	for _, definition := range []string{`{}`, `{"description":"Arbitrary JSON"}`, `{"type":"object"}`, `{"type":"object","additionalProperties":true}`, `{"type":"object","additionalProperties":{"type":"string"}}`} {
		t.Run(definition, func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"Update configuration"}],"tools":[{"name":"configuration","input_schema":{"type":"object","properties":{"requestBody":{"type":"object","properties":{"definition":` + definition + `},"required":["definition"]}},"required":["requestBody"]}}]}`)
			envelope, err := ParseAnthropic(body)
			require.NoError(t, err)
			prepared, err := envelope.PrepareOpenAIResponses(nil, EmitOptions{TargetModel: "gpt-5.6-luna"})
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(prepared.Body, "tools.0.strict").Bool())
			require.JSONEq(t, definition, gjson.GetBytes(prepared.Body, "tools.0.parameters.properties.requestBody.properties.definition").Raw)
		})
	}
}

func TestStrictify_ObjectTypeUnionBails(t *testing.T) {
	_, ok := strictifyFromJSON(t, `{"type":"object","properties":{"definition":{"type":["object","array","null"],"items":{"type":"string"}}},"required":["definition"]}`)
	require.False(t, ok, "object-capable type unions cannot silently acquire strict object constraints")
}
