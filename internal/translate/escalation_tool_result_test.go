package translate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

func TestCodexToolResultFailures(t *testing.T) {
	const failure = "Chunk ID: abc123\nWall time: 0.0001 seconds\nProcess exited with code 7\nOriginal token count: 1\nOutput:\nsynthetic output\n"
	const success = "Wall time: 1.2500 seconds\nProcess exited with code 0\nOutput:\nProcess exited with code 7\n"
	failed, succeeded := true, false
	for _, fixture := range []struct {
		name      string
		tool      string
		namespace string
		output    any
		explicit  *bool
		failed    bool
	}{
		{name: "exec nonzero", tool: "exec_command", output: failure, failed: true},
		{name: "stdin nonzero", tool: "write_stdin", output: failure, failed: true},
		{name: "functions namespace", tool: "exec_command", namespace: "functions", output: failure, failed: true},
		{name: "client namespace", tool: "exec_command", namespace: "client_tools", output: failure},
		{name: "negative status", tool: "exec_command", output: strings.Replace(failure, "code 7", "code -1", 1), failed: true},
		{name: "content item", tool: "exec_command", output: []map[string]string{{"type": "input_text", "text": failure}}, failed: true},
		{name: "success with failure in stdout", tool: "exec_command", output: success},
		{name: "running", tool: "write_stdin", output: "Chunk ID: abc123\nWall time: 1.0000 seconds\nProcess running with session ID 42\nOutput:\nProcess exited with code 7\n"},
		{name: "stdout without envelope", tool: "exec_command", output: "Process exited with code 7\n"},
		{name: "header quoted in stdout", tool: "exec_command", output: "Wall time: 0.1 seconds\nOutput:\n" + failure},
		{name: "missing output delimiter", tool: "exec_command", output: "Wall time: 0.1 seconds\nProcess exited with code 7\n"},
		{name: "out of range", tool: "exec_command", output: strings.Replace(failure, "code 7", "code 999999999999", 1)},
		{name: "fractional status", tool: "exec_command", output: strings.Replace(failure, "code 7", "code 0.5", 1)},
		{name: "unrelated tool", tool: "read_file", output: failure},
		{name: "structured application output", tool: "exec_command", output: map[string]int{"exit_code": 7}},
		{name: "unknown content type", tool: "exec_command", output: []map[string]string{{"type": "text", "text": failure}}},
		{name: "multiple content blocks", tool: "exec_command", output: []map[string]string{{"type": "input_text", "text": failure}, {"type": "input_text", "text": "other"}}},
		{name: "explicit true", tool: "read_file", output: "any", explicit: &failed, failed: true},
		{name: "explicit false overrides header", tool: "exec_command", output: failure, explicit: &succeeded},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			encoded, err := json.Marshal(fixture.output)
			require.NoError(t, err)
			block := translate.EscalationBlock{Type: translate.EscalationBlockToolResult, ContentJSON: string(encoded), IsError: fixture.explicit}
			original := block
			call := translate.EscalationBlock{Type: translate.EscalationBlockToolCall, Name: fixture.tool, Namespace: fixture.namespace}
			require.Equal(t, fixture.failed, block.ToolResultFailed(call, true))
			require.Equal(t, fixture.explicit != nil && *fixture.explicit, block.ToolResultFailed(call, false), "other clients require explicit protocol error flags")
			require.Equal(t, original, block, "feature extraction must not alter replay identity")
		})
	}
}
