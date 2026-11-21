package proxy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	switchExperimentMid  = "claude-sonnet-5-5"
	switchExperimentHigh = "claude-opus-5-5"
)

type recordingThreadCompactor struct {
	models []string
}

func (c *recordingThreadCompactor) CompactionOn(model string) handover.Summarizer {
	c.models = append(c.models, model)
	return fixedSummary("SUMMARY: task, constraints and edits so far")
}

type fixedSummary string

func (f fixedSummary) Summarize(context.Context, *translate.RequestEnvelope, router.Request) (string, handover.Usage, error) {
	return string(f), handover.Usage{}, nil
}

func (fixedSummary) Provider() string { return "anthropic" }

func largeAgentTurn(t *testing.T, rounds int, firstPrompt string) *translate.RequestEnvelope {
	t.Helper()
	msgs := []any{map[string]any{"role": "user", "content": firstPrompt}}
	file := strings.Repeat("x = compute(y)  # line\n", 2_600)
	for i := range rounds {
		id := fmt.Sprintf("toolu_%02d", i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "bash", "input": map[string]any{"command": fmt.Sprintf("cat file_%d.py", i)}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": file}}},
		)
	}
	body, err := json.Marshal(map[string]any{"model": "m", "max_tokens": 1024, "messages": msgs})
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	return env
}

func applyExperimentTurn(x *switchExperiment, env *translate.RequestEnvelope, fresh string, now time.Time) turnLoopResult {
	res := turnLoopResult{SessionKey: [sessionpin.SessionKeyLen]byte{7}, Fresh: router.Decision{Model: fresh}, Decision: router.Decision{Model: fresh}}
	x.apply(context.Background(), env, router.Request{}, now, true, &res)
	return res
}

func firstMessageIsSummary(t *testing.T, env *translate.RequestEnvelope) bool {
	t.Helper()
	out, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: switchExperimentHigh})
	require.NoError(t, err)
	return strings.Contains(string(out.Body), translate.HandoverSummaryTag)
}

func TestSwitchExperiment_CompactionStaysOnEveryLaterTurn(t *testing.T) {
	const prompt = "fix the parser bug; never edit test files"
	compactor := &recordingThreadCompactor{}
	x := &switchExperiment{
		cfg:         SwitchExperimentConfig{Live: true, CompactTail: 4, Tuning: routingpolicy.SwitchTuning{DowngradeHorizonTurns: 10, UpgradeHorizonTurns: 25}},
		compactor:   compactor,
		compactions: conversation.NewMemoryCompactionStore(time.Hour),
		threads:     map[string]switchExperimentThread{},
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	applyExperimentTurn(x, largeAgentTurn(t, 10, prompt), switchExperimentMid, now)

	switchTurn := largeAgentTurn(t, 11, prompt)
	fullMessages := switchTurn.MessageCount()
	res := applyExperimentTurn(x, switchTurn, switchExperimentHigh, now.Add(time.Minute))
	require.Equal(t, "switch_experiment_"+string(routingpolicy.SwitchReasonCompactThenSwitch), res.PinTier)
	assert.Equal(t, switchExperimentHigh, res.Decision.Model)
	assert.Equal(t, []string{switchExperimentMid}, compactor.models, "summary must run on the warm current model")
	assert.Less(t, switchTurn.MessageCount(), fullMessages)
	assert.True(t, firstMessageIsSummary(t, switchTurn))

	for i, rounds := range []int{12, 13} {
		later := largeAgentTurn(t, rounds, prompt)
		resent := later.MessageCount()
		res = applyExperimentTurn(x, later, switchExperimentHigh, now.Add(time.Duration(2+i)*time.Minute))
		assert.Equal(t, switchExperimentHigh, res.Decision.Model)
		assert.Less(t, later.MessageCount(), resent, "turn with %d rounds must reuse the stored compaction", rounds)
		assert.True(t, firstMessageIsSummary(t, later))
	}
	assert.Len(t, compactor.models, 1, "later turns reuse the stored summary instead of summarizing again")

	diverged := largeAgentTurn(t, 13, "a different task")
	resent := diverged.MessageCount()
	applyExperimentTurn(x, diverged, switchExperimentHigh, now.Add(5*time.Minute))
	assert.Equal(t, resent, diverged.MessageCount(), "a history that no longer extends the anchor is served in full")
	assert.False(t, firstMessageIsSummary(t, diverged))
	_, kept := x.compactions.Load(hex.EncodeToString(append([]byte{7}, make([]byte, sessionpin.SessionKeyLen-1)...)), now.Add(5*time.Minute))
	assert.False(t, kept, "a stale compaction is cleared")
}
