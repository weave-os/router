package proxy

import (
	"encoding/json"
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

const toolUseObserverJSONBody = `{"id":"msg_1","type":"message","role":"assistant","stop_reason":"tool_use",` +
	`"content":[{"type":"text","text":"ok"},` +
	`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a.go"}},` +
	`{"type":"tool_use","id":"toolu_2","name":"Edit","input":{"path":"a.go","old":"x","new":"yy"}}],` +
	`"usage":{"input_tokens":1,"output_tokens":2}}`

// toolUseObservation is the state terminal() reads; comparing it after a write
// boundary shows the observer saw the same frames a single write would have.
type toolUseObservation struct {
	stopReason string
	last       lastToolUse
	lastIndex  int64
	haveLast   bool
}

func observationOf(o *toolUseObserver) toolUseObservation {
	return toolUseObservation{stopReason: o.stopReason, last: o.last, lastIndex: o.lastIndex, haveLast: o.haveLast}
}

// toolUseObserverLargeFixture streams one Bash call whose input JSON is far
// larger than a provider read, so the input_json_delta frame spans many writes.
func toolUseObserverLargeFixture(t *testing.T) (body string, inputBytes int) {
	t.Helper()
	input := `{"command":"` + strings.Repeat("x", 100*1024) + `"}`
	partialJSON, err := json.Marshal(input)
	require.NoError(t, err)
	body = "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_9","name":"Bash","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + string(partialJSON) + `}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}` + "\n\n"
	return body, len(input)
}

// However the wire splits the response, terminal() must name the same tool with
// the same input size a single write reports, and the observer's view after any
// write boundary must match a single write of that prefix.
func TestToolUseObserver_FragmentationMatchesSingleWrite(t *testing.T) {
	large, largeInput := toolUseObserverLargeFixture(t)
	fixtures := []struct {
		name string
		body string
		want lastToolUse
	}{
		{name: "lf stream", body: toolUseObserverStreamFixture, want: lastToolUse{Name: "Bash", InputBytes: len(toolUseObserverBashInput)}},
		{name: "crlf stream", body: strings.ReplaceAll(toolUseObserverStreamFixture, "\n", "\r\n"), want: lastToolUse{Name: "Bash", InputBytes: len(toolUseObserverBashInput)}},
		{name: "input spanning many writes", body: large, want: lastToolUse{Name: "Bash", InputBytes: largeInput}},
		{name: "non-streaming body", body: toolUseObserverJSONBody, want: lastToolUse{Name: "Edit", InputBytes: len(`{"path":"a.go","old":"x","new":"yy"}`)}},
	}
	prefixObservation := func(t *testing.T, prefix string) toolUseObservation {
		t.Helper()
		obs := newToolUseObserver(httptest.NewRecorder())
		writeChunks(t, obs, []string{prefix})
		return observationOf(obs)
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			oracle := newToolUseObserver(httptest.NewRecorder())
			writeChunks(t, oracle, []string{fx.body})
			got, ok := oracle.terminal()
			require.True(t, ok)
			require.Equal(t, fx.want, got)

			for _, size := range chunkSizesFor(fx.body) {
				rec := httptest.NewRecorder()
				obs := newToolUseObserver(rec)
				writeChunks(t, obs, chunkEvery(fx.body, size))
				last, ok := obs.terminal()
				assert.True(t, ok, "chunk size %d", size)
				assert.Equal(t, fx.want, last, "chunk size %d", size)
				assert.Equal(t, fx.body, rec.Body.String(), "chunk size %d", size)
			}

			if len(fx.body) > largeFixtureBytes {
				return
			}
			for i := 1; i < len(fx.body); i++ {
				rec := httptest.NewRecorder()
				obs := newToolUseObserver(rec)
				writeChunks(t, obs, []string{fx.body[:i]})
				require.Equal(t, prefixObservation(t, fx.body[:i]), observationOf(obs), "state after the first write, split at %d", i)
				writeChunks(t, obs, []string{fx.body[i:]})
				last, ok := obs.terminal()
				require.True(t, ok, "split at %d", i)
				require.Equal(t, fx.want, last, "split at %d", i)
				require.Equal(t, fx.body, rec.Body.String(), "split at %d", i)
			}
		})
	}
}
