package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func agentTurnBody(t *testing.T, rounds int, firstPrompt string) []byte {
	t.Helper()
	msgs := []any{map[string]any{"role": "user", "content": firstPrompt}}
	for i := range rounds {
		id := fmt.Sprintf("toolu_%02d", i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "bash", "input": map[string]any{"command": fmt.Sprintf("cat file_%d.py", i)}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": fmt.Sprintf("contents of file %d", i)}}},
		)
	}
	body, err := json.Marshal(map[string]any{"model": "m", "max_tokens": 1024, "messages": msgs})
	require.NoError(t, err)
	return body
}

func parseAgentTurn(t *testing.T, body []byte) *translate.RequestEnvelope {
	t.Helper()
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	return env
}

func emittedMessages(t *testing.T, env *translate.RequestEnvelope) []gjson.Result {
	t.Helper()
	out, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "m"})
	require.NoError(t, err)
	return gjson.GetBytes(out.Body, "messages").Array()
}

func TestThreadCompaction_SubstitutesSummaryOnEveryLaterTurn(t *testing.T) {
	store := NewMemoryCompactionStore(time.Hour)
	const key = "session-a"
	const prompt = "fix the failing parser test and keep the public API unchanged"

	switchTurn := parseAgentTurn(t, agentTurnBody(t, 10, prompt))
	compaction, ok := PlanThreadCompaction(switchTurn, 4, "SUMMARY: parser bug is in tokenize()", "model-a", time.Now())
	require.True(t, ok)
	store.Save(key, compaction)

	for _, rounds := range []int{10, 11, 14} {
		env := parseAgentTurn(t, agentTurnBody(t, rounds, prompt))
		stored, found := store.Load(key, time.Now())
		require.True(t, found)
		require.True(t, ApplyThreadCompaction(env, stored), "turn with %d rounds must reuse the compaction", rounds)

		msgs := emittedMessages(t, env)
		require.NotEmpty(t, msgs)
		assert.Equal(t, "user", msgs[0].Get("role").String())
		assert.Contains(t, msgs[0].Raw, "SUMMARY: parser bug is in tokenize()")
		assert.NotContains(t, gjson.Parse("["+joinRaw(msgs)+"]").Raw, "contents of file 0", "pre-anchor history must not be resent")
		assert.Len(t, msgs, 1+2*rounds+1-compaction.AnchorMessages)
		assert.Equal(t, "assistant", msgs[1].Get("role").String(), "tail must start at a tool_use turn so no tool_result is orphaned")
	}
}

func TestThreadCompaction_IgnoresDivergedHistory(t *testing.T) {
	compaction, ok := PlanThreadCompaction(parseAgentTurn(t, agentTurnBody(t, 10, "original task")), 4, "SUMMARY", "model-a", time.Now())
	require.True(t, ok)

	clientCompacted := parseAgentTurn(t, agentTurnBody(t, 3, "client-side compacted continuation"))
	assert.False(t, ApplyThreadCompaction(clientCompacted, compaction))
	assert.Contains(t, joinRaw(emittedMessages(t, clientCompacted)), "client-side compacted continuation")

	shorter := parseAgentTurn(t, agentTurnBody(t, 2, "original task"))
	assert.False(t, ApplyThreadCompaction(shorter, compaction))
}

func TestThreadCompaction_SkipsShortThreads(t *testing.T) {
	_, ok := PlanThreadCompaction(parseAgentTurn(t, agentTurnBody(t, 2, "task")), 8, "SUMMARY", "model-a", time.Now())
	assert.False(t, ok)
}

func TestMemoryCompactionStore_ExpiresAfterIdleTTLAndClears(t *testing.T) {
	store := NewMemoryCompactionStore(time.Minute)
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store.Save("k", ThreadCompaction{AnchorMessages: 3, CreatedAt: start, UsedAt: start})
	for i := 1; i <= 3; i++ {
		_, found := store.Load("k", start.Add(time.Duration(i)*50*time.Second))
		assert.True(t, found, "an active thread keeps its compaction past the TTL since creation")
	}
	_, found := store.Load("k", start.Add(150*time.Second+2*time.Minute))
	assert.False(t, found, "an idle thread's compaction expires")

	store.Save("k", ThreadCompaction{AnchorMessages: 3, UsedAt: start})
	store.Clear("k")
	_, found = store.Load("k", start)
	assert.False(t, found)
}

func joinRaw(msgs []gjson.Result) string {
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = m.Raw
	}
	return strings.Join(parts, ",")
}
