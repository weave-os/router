package translate

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexReportedToolTiming(t *testing.T) {
	const completed = "Script completed\nWall time 2.2 seconds\nOutput:\n"
	for _, fixture := range []struct {
		name      string
		tool      codexExecTool
		callType  escalationWireType
		namespace string
		output    any
		want      time.Duration
		reported  bool
		yielded   bool
	}{
		{name: "captured code-mode shape", tool: codexCodeExec, namespace: responsesDefaultToolNamespace, output: []map[string]any{{"type": escalationWireInputText, "text": completed}, {"type": escalationWireInputText, "text": `{"wall_time_seconds":999}`}}, want: 2200 * time.Millisecond, reported: true},
		{name: "string wrapper", tool: codexCodeExec, output: completed, want: 2200 * time.Millisecond, reported: true},
		{name: "wait wrapper", tool: codexCodeWait, output: completed, want: 2200 * time.Millisecond, reported: true},
		{name: "native completed", tool: codexExecCommand, callType: escalationWireFunctionCall, output: "Chunk ID: synthetic\nWall time: 2.2 seconds\nProcess exited with code 0\nOriginal token count: 1\nOutput:\n", want: 2200 * time.Millisecond, reported: true},
		{name: "native yielded", tool: codexExecCommand, callType: escalationWireFunctionCall, output: "Wall time: 1.0017 seconds\nProcess running with session ID 42\nOutput:\n", want: 1001700 * time.Microsecond, reported: true, yielded: true},
		{name: "native poll", tool: codexWriteStdin, callType: escalationWireFunctionCall, output: "Wall time: 0.5 seconds\nProcess exited with code 0\nOutput:\n", want: 500 * time.Millisecond, reported: true},
		{name: "zero runtime", tool: codexCodeExec, output: "Script completed\nWall time 0 seconds\nOutput:\n", reported: true},
		{name: "foreign namespace", tool: codexCodeExec, namespace: "client_tools", output: completed},
		{name: "legacy tool is not code-mode", tool: codexExecCommand, output: completed},
		{name: "no wrapper", tool: codexCodeExec, output: `{"wall_time_seconds":2.2}`},
		{name: "quoted header", tool: codexCodeExec, output: "Output:\n" + completed},
		{name: "negative runtime", tool: codexCodeExec, output: "Script completed\nWall time -2 seconds\nOutput:\n"},
		{name: "overflow runtime", tool: codexCodeExec, output: "Script completed\nWall time 99999999999999999999999 seconds\nOutput:\n"},
		{name: "running script", tool: codexCodeExec, output: "Script running with cell ID 1\nWall time 2.2 seconds\nOutput:\n"},
		{name: "empty fragments", tool: codexCodeExec, output: []any{}},
		{name: "wrong fragment type", tool: codexCodeExec, output: []map[string]any{{"type": escalationWireOutputText, "text": completed}}},
		{name: "header in nested output", tool: codexCodeExec, output: []map[string]any{{"type": escalationWireInputText, "text": "ordinary output"}, {"type": escalationWireInputText, "text": completed}}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			callType, outputType := fixture.callType, escalationWireFunctionOutput
			if callType == "" {
				callType, outputType = escalationWireCustomCall, escalationWireCustomOutput
			}
			body, err := json.Marshal(map[string]any{"input": []map[string]any{
				{"type": callType, "call_id": "call_timing", "name": fixture.tool, "namespace": fixture.namespace, "input": "synthetic code"},
				{"type": outputType, "call_id": "call_timing", "output": fixture.output},
			}})
			require.NoError(t, err)
			timing, reported := CodexReportedToolTiming(body)
			require.Equal(t, fixture.reported, reported)
			if reported {
				require.Equal(t, fixture.want, timing.CallDuration)
				require.Equal(t, fixture.yielded, timing.Yielded)
			}
		})
	}
}

func TestCodexReportedToolTiming_OnlyLatestResult(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		body     string
		reported bool
	}{
		{name: "new user input", body: `{"input":[{"type":"custom_tool_call","call_id":"a","name":"exec"},{"type":"custom_tool_call_output","call_id":"a","output":"Script completed\nWall time 2.2 seconds\nOutput:\n"},{"type":"message","role":"user","content":"next"}]}`},
		{name: "unmatched call", body: `{"input":[{"type":"custom_tool_call","call_id":"a","name":"exec"},{"type":"custom_tool_call_output","call_id":"b","output":"Script completed\nWall time 2.2 seconds\nOutput:\n"}]}`},
		{name: "missing call ID", body: `{"input":[{"type":"custom_tool_call","name":"exec"},{"type":"custom_tool_call_output","output":"Script completed\nWall time 2.2 seconds\nOutput:\n"}]}`},
		{name: "parallel results are not summed", body: `{"input":[{"type":"custom_tool_call","call_id":"a","name":"exec"},{"type":"custom_tool_call","call_id":"b","name":"exec"},{"type":"custom_tool_call_output","call_id":"a","output":"Script completed\nWall time 9 seconds\nOutput:\n"},{"type":"custom_tool_call_output","call_id":"b","output":"Script completed\nWall time 2.2 seconds\nOutput:\n"}]}`, reported: true},
		{name: "no input", body: `{}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			timing, reported := CodexReportedToolTiming([]byte(fixture.body))
			require.Equal(t, fixture.reported, reported)
			if reported {
				require.Equal(t, 2200*time.Millisecond, timing.CallDuration)
			}
		})
	}
}
