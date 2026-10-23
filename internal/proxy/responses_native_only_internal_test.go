package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

// The log must name the tool or input item that pinned the request, or a
// dropped cross-provider pin cannot be traced to its cause.
func TestNativeOnlyReasons_NamesPreservedTransforms(t *testing.T) {
	body := []byte(`{"model":"auto","input":"hi","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{}}},{"type":"web_search"}]}`)
	conversion, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	require.True(t, conversion.Requirements.NativeOnly)

	assert.Equal(t, []string{"responses_unknown_tool_native_only@tools.1:web_search"}, nativeOnlyReasons(conversion.Report, body))
}

func TestNativeOnlyReasons_Bounded(t *testing.T) {
	report := make([]translate.ResponseTransform, nativeOnlyReasonLimit+3)
	for i := range report {
		report[i] = translate.ResponseTransform{Code: "responses_unknown_tool_native_only", Action: "preserved", Path: "tools"}
	}
	report = append(report, translate.ResponseTransform{Code: "responses_developer_message_projected", Action: "projected", Path: "input.0.role"})

	assert.Len(t, nativeOnlyReasons(report, nil), nativeOnlyReasonLimit)
}
