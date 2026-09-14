package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const toolUseObserverStreamFixture = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"looking\"}}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Read\",\"input\":{}}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"a.go\\\"}\"}}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_2\",\"name\":\"Bash\",\"input\":{}}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"go test ./...\\\"}\"}}\n\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":12}}\n\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

// The Bash input JSON reassembled from its two input_json_delta frames.
const toolUseObserverBashInput = `{"command":"go test ./..."}`

func writeInChunks(t *testing.T, obs *toolUseObserver, body string, chunk int) {
	t.Helper()
	for i := 0; i < len(body); i += chunk {
		end := i + chunk
		if end > len(body) {
			end = len(body)
		}
		_, err := obs.Write([]byte(body[i:end]))
		require.NoError(t, err)
	}
}

func TestToolUseObserver_StreamingToolUseStopNamesLastTool(t *testing.T) {
	rec := httptest.NewRecorder()
	obs := newToolUseObserver(rec)
	// Odd chunk size so every frame boundary lands mid-frame at least once.
	writeInChunks(t, obs, toolUseObserverStreamFixture, 7)

	last, ok := obs.terminal()
	require.True(t, ok)
	assert.Equal(t, lastToolUse{Name: "Bash", InputBytes: len(toolUseObserverBashInput)}, last)
	assert.Equal(t, toolUseObserverStreamFixture, rec.Body.String(), "observer must pass bytes through unchanged")
}

func TestToolUseObserver_StreamCutBeforeMessageDeltaIsUnknown(t *testing.T) {
	obs := newToolUseObserver(httptest.NewRecorder())
	cut := toolUseObserverStreamFixture[:strings.Index(toolUseObserverStreamFixture, "event: message_delta")]
	writeInChunks(t, obs, cut, 64)

	_, ok := obs.terminal()
	assert.False(t, ok)
}

func TestToolUseObserver_EndTurnStopIsUnknown(t *testing.T) {
	obs := newToolUseObserver(httptest.NewRecorder())
	turn := strings.Replace(toolUseObserverStreamFixture, `"stop_reason":"tool_use"`, `"stop_reason":"end_turn"`, 1)
	writeInChunks(t, obs, turn, 64)

	_, ok := obs.terminal()
	assert.False(t, ok, "a tool_use block on a non-tool_use stop is not the terminal call")
}

func TestToolUseObserver_NonStreamingBodyUsesLastToolUseBlock(t *testing.T) {
	obs := newToolUseObserver(httptest.NewRecorder())
	body := `{"id":"msg_1","type":"message","role":"assistant","stop_reason":"tool_use",` +
		`"content":[{"type":"text","text":"ok"},` +
		`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a.go"}},` +
		`{"type":"tool_use","id":"toolu_2","name":"Edit","input":{"path":"a.go","old":"x","new":"yy"}}],` +
		`"usage":{"input_tokens":1,"output_tokens":2}}`
	writeInChunks(t, obs, body, 33)

	last, ok := obs.terminal()
	require.True(t, ok)
	assert.Equal(t, lastToolUse{Name: "Edit", InputBytes: len(`{"path":"a.go","old":"x","new":"yy"}`)}, last)
}

func TestToolUseObserver_ErrorBodyIsUnknown(t *testing.T) {
	obs := newToolUseObserver(httptest.NewRecorder())
	_, err := obs.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	require.NoError(t, err)

	_, ok := obs.terminal()
	assert.False(t, ok)
}

func TestToolUseObserver_OversizedJSONBodyIsUnknown(t *testing.T) {
	obs := newToolUseObserver(httptest.NewRecorder())
	padding := strings.Repeat("x", toolUseScanCap)
	body := `{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash","input":{"pad":"` + padding + `"}}]}`
	writeInChunks(t, obs, body, 1<<16)

	_, ok := obs.terminal()
	assert.False(t, ok)
	assert.Nil(t, obs.jsonBody, "over-cap body must be released, not held")
}

func TestToolUseObserver_NilIsUnknown(t *testing.T) {
	var obs *toolUseObserver
	_, ok := obs.terminal()
	assert.False(t, ok)
}

func TestToolErrorCounts_TalliesResolvedCallsPerTool(t *testing.T) {
	outcomes := []translate.ToolCallOutcome{
		{Name: "Bash", Resolved: true, Errored: true},
		{Name: "Bash", Resolved: true, Errored: false},
		{Name: "Read", Resolved: true, Errored: false},
		{Name: "Bash", Resolved: true, Errored: true},
		// The in-flight call at the end of a tool_use turn has no result yet.
		{Name: "Edit", Resolved: false},
	}

	counts := toolErrorCounts(outcomes)
	assert.Equal(t, map[string]toolCallTally{
		"Bash": {Calls: 3, Errors: 2},
		"Read": {Calls: 1, Errors: 0},
	}, counts)
	assert.JSONEq(t, `{"Bash":{"calls":3,"errors":2},"Read":{"calls":1,"errors":0}}`, string(toolErrorCountsJSON(counts)))
}

func TestToolErrorCounts_NoResolvedCallsStaysNil(t *testing.T) {
	assert.Nil(t, toolErrorCounts(nil))
	assert.Nil(t, toolErrorCounts([]translate.ToolCallOutcome{{Name: "Edit", Resolved: false}}))
	assert.Nil(t, toolErrorCountsJSON(nil))
}
