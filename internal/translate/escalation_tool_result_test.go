package translate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

const scriptCompleted = "Script completed\nWall time 1.3 seconds\nOutput:\n"
const codeModeSuccess = `{"chunk_id":"ab11","wall_time_seconds":0.1,"exit_code":0,"original_token_count":1,"output":"ok"}`

func codeMode(fragments ...string) []map[string]string {
	content := make([]map[string]string, 0, len(fragments))
	for _, fragment := range fragments {
		content = append(content, map[string]string{"type": "input_text", "text": fragment})
	}
	return content
}

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
		{name: "code mode nonzero exit", tool: "exec", output: codeMode(scriptCompleted, `{"chunk_id":"d9d08a","wall_time_seconds":0.1,"exit_code":1,"original_token_count":2,"output":"synthetic"}`), failed: true},
		{name: "code mode truncated nonzero exit", tool: "exec", output: codeMode(scriptCompleted, "Warning: truncated output (original token count: 9)\nTotal output lines: 2\n\n"+`{"chunk_id":"ab12","wall_time_seconds":0.1,"exit_code":2,"original_token_count":9,"output":"synth`), failed: true},
		{name: "code mode later command fails", tool: "exec", output: codeMode(scriptCompleted, codeModeSuccess, `{"chunk_id":"ab13","wall_time_seconds":0.1,"exit_code":-1,"original_token_count":1,"output":""}`), failed: true},
		{name: "code mode script failed", tool: "exec", output: codeMode("Script failed\nWall time 0.0 seconds\nOutput:\n", "Script error:\nsynthetic verification failed"), failed: true},
		{name: "code mode string script failed", tool: "exec", output: "Script failed\nWall time 0.0 seconds\nOutput:\n", failed: true},
		{name: "code mode success", tool: "exec", output: codeMode(scriptCompleted, codeModeSuccess)},
		{name: "code mode running", tool: "exec", output: codeMode("Script running with cell ID 5\nWall time 31.0 seconds\nOutput:\n", `{"chunk_id":"ec61","wall_time_seconds":1.0,"session_id":6,"original_token_count":0,"output":""}`)},
		{name: "code mode failure quoted in stdout", tool: "exec", output: codeMode(scriptCompleted, `{"chunk_id":"ab14","wall_time_seconds":0.1,"exit_code":0,"original_token_count":1,"output":"{\"chunk_id\":\"x\",\"wall_time_seconds\":1,\"exit_code\":7,"}`)},
		{name: "code mode printed failure header", tool: "exec", output: codeMode(scriptCompleted, "Script failed\nWall time 0.0 seconds\nOutput:\n")},
		{name: "code mode envelope without script header", tool: "exec", output: codeMode(`{"chunk_id":"ab15","wall_time_seconds":0.1,"exit_code":1,"original_token_count":1,"output":""}`)},
		{name: "code mode client namespace", tool: "exec", namespace: "client_tools", output: codeMode(scriptCompleted, `{"chunk_id":"ab16","wall_time_seconds":0.1,"exit_code":1,"original_token_count":1,"output":""}`)},
		{name: "code mode structured output", tool: "exec", output: map[string]int{"exit_code": 7}},
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
