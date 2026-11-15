package conversation

import (
	"encoding/json"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

type toolCall struct {
	name  string
	input map[string]any
}

func buildBodyWithToolCalls(t *testing.T, calls []toolCall) []byte {
	t.Helper()
	msgs := []any{
		map[string]any{"role": "user", "content": "do the thing"},
	}
	for i, c := range calls {
		id := "toolu_" + itoa(i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": id, "name": c.name, "input": c.input},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": id, "content": "result"},
			}},
		)
	}
	body, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 256,
		"messages":   msgs,
	})
	require.NoError(t, err)
	return body
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string('0'+byte(n%10)) + digits
		n /= 10
	}
	return digits
}

// ---- cyclic (wide re-read) loop detector ----

func cyclicReads(nFiles, total int) []toolCall {
	calls := make([]toolCall, 0, total)
	for i := 0; i < total; i++ {
		calls = append(calls, toolCall{name: "Read", input: map[string]any{"file_path": "/app/f" + itoa(i%nFiles) + ".go"}})
	}
	return calls
}

func TestDetectCyclicToolCallLoop_TripsOnLowDiversityCycle(t *testing.T) {
	// 30 Reads cycling over 5 files (each 6x) → distinct ratio 5/30 ≈ 0.17 < 0.4.
	env, err := translate.ParseAnthropic(buildBodyWithToolCalls(t, cyclicReads(5, 30)))
	require.NoError(t, err)
	looped, top, count, ratio, total := DetectCyclicToolCallLoop(env)
	assert.True(t, looped, "low-diversity re-read cycle must trip")
	assert.Equal(t, "Read", top.Name)
	assert.GreaterOrEqual(t, count, 2)
	assert.Less(t, ratio, cyclicLoopMaxDistinctRatio)
	assert.Equal(t, cyclicLoopWindowSize, total)
}

func TestDetectCyclicToolCallLoop_BroadDistinctReadsDoNotTrip(t *testing.T) {
	// A healthy Explore reads MANY DISTINCT files → high diversity → no trip.
	env, err := translate.ParseAnthropic(buildBodyWithToolCalls(t, cyclicReads(30, 30)))
	require.NoError(t, err)
	looped, _, _, ratio, _ := DetectCyclicToolCallLoop(env)
	assert.False(t, looped, "broad distinct exploration must not trip (the #271 guard)")
	assert.GreaterOrEqual(t, ratio, cyclicLoopMaxDistinctRatio)
}

func TestDetectCyclicToolCallLoop_EditInWindowIsProgress(t *testing.T) {
	// Same low-diversity cycle but with a real Edit in the window → progress, no trip.
	calls := cyclicReads(5, 29)
	calls = append(calls, toolCall{name: "Edit", input: map[string]any{"file_path": "/app/f0.go", "old_string": "a", "new_string": "b"}})
	env, err := translate.ParseAnthropic(buildBodyWithToolCalls(t, calls))
	require.NoError(t, err)
	looped, _, _, _, _ := DetectCyclicToolCallLoop(env)
	assert.False(t, looped, "an edit in the window means the agent is progressing, not stuck")
}

func TestDetectCyclicToolCallLoop_BelowMinCallsDoesNotTrip(t *testing.T) {
	// Fewer than cyclicLoopMinCalls tool calls → too early to call it a loop.
	env, err := translate.ParseAnthropic(buildBodyWithToolCalls(t, cyclicReads(3, 20)))
	require.NoError(t, err)
	looped, _, _, _, _ := DetectCyclicToolCallLoop(env)
	assert.False(t, looped, "below the min-calls floor must not trip")
}

func TestDetectCyclicToolCallLoop_LaunchPlusPollingDoesNotTrip(t *testing.T) {
	// Codex launches a long-running command once, then repeats empty
	// write_stdin polls. The launch makes the window mixed, but the polls are
	// still legitimate and must not count as a re-read cycle.
	calls := []toolCall{{name: "exec", input: map[string]any{"input": "sleep 60"}}}
	poll := `const r = await tools.write_stdin({"session_id":72385,"chars":"","yield_time_ms":1000,"max_output_tokens":12000});`
	for len(calls) < cyclicLoopWindowSize {
		calls = append(calls, toolCall{name: "exec", input: map[string]any{"input": poll}})
	}
	env, err := translate.ParseAnthropic(buildBodyWithToolCalls(t, calls))
	require.NoError(t, err)
	looped, _, _, _, total := DetectCyclicToolCallLoop(env)
	assert.False(t, looped, "a command followed by empty write_stdin polls must not trip")
	assert.Equal(t, cyclicLoopWindowSize, total)
}
