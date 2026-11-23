package proxy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

func (*recordingThreadCompactor) AllowsCompaction(string) bool { return true }

func (c *recordingThreadCompactor) CompactionOn(model string, _ int) handover.Summarizer {
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
		prepared:    map[string]*preparedCompaction{},
		spawn:       runInline,
		now:         time.Now,
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

const switchExperimentLow = "claude-haiku-5-5"

type allowlistCompactor struct {
	mu            sync.Mutex
	allowed       map[string]bool
	models        []string
	summaryTokens []int
	summary       handover.Summarizer
}

func (c *allowlistCompactor) AllowsCompaction(model string) bool { return c.allowed[model] }

func (c *allowlistCompactor) CompactionOn(model string, summaryTokens int) handover.Summarizer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = append(c.models, model)
	c.summaryTokens = append(c.summaryTokens, summaryTokens)
	return c.summary
}

func (c *allowlistCompactor) summarizers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.models...)
}

type gatedSummary struct{ release chan struct{} }

func (g gatedSummary) Summarize(ctx context.Context, _ *translate.RequestEnvelope, _ router.Request) (string, handover.Usage, error) {
	select {
	case <-g.release:
		return "SUMMARY: task, constraints and edits so far", handover.Usage{}, nil
	case <-ctx.Done():
		return "", handover.Usage{}, ctx.Err()
	}
}

func (gatedSummary) Provider() string { return "anthropic" }

func newPreparingExperiment(compactor ThreadCompactor, timeout time.Duration, spawn func(context.Context, time.Duration, func(context.Context))) *switchExperiment {
	return &switchExperiment{
		cfg: SwitchExperimentConfig{
			Live: true, CompactTail: 4, CompactTimeout: timeout,
			Tuning: routingpolicy.SwitchTuning{DowngradeHorizonTurns: 10, UpgradeHorizonTurns: 25},
		},
		compactor:   compactor,
		compactions: conversation.NewMemoryCompactionStore(time.Hour),
		threads:     map[string]switchExperimentThread{},
		prepared:    map[string]*preparedCompaction{},
		spawn:       spawn,
		now:         time.Now,
	}
}

func runInline(parent context.Context, timeout time.Duration, fn func(context.Context)) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	fn(ctx)
}

func TestSwitchExperiment_SummarizesOnAnAllowedWarmModelWhenTheCurrentOneIsNotAllowed(t *testing.T) {
	const prompt = "fix the parser bug; never edit test files"
	compactor := &allowlistCompactor{allowed: map[string]bool{switchExperimentMid: true, switchExperimentHigh: true}, summary: fixedSummary("SUMMARY")}
	x := newPreparingExperiment(compactor, time.Second, runInline)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	applyExperimentTurn(x, largeAgentTurn(t, 10, prompt), switchExperimentMid, now)
	applyExperimentTurn(x, largeAgentTurn(t, 11, prompt), switchExperimentLow, now.Add(30*time.Second))
	applyExperimentTurn(x, largeAgentTurn(t, 12, prompt), switchExperimentLow, now.Add(time.Minute))

	require.NotEmpty(t, compactor.summarizers())
	assert.NotContains(t, compactor.summarizers(), switchExperimentLow, "a model outside the compaction catalog never summarizes")
	assert.Equal(t, switchExperimentMid, compactor.summarizers()[0], "the warm allowed model summarizes")
}

func TestSwitchExperiment_PreparesTheSummaryOffTheUserTurn(t *testing.T) {
	const prompt = "fix the parser bug; never edit test files"
	release := make(chan struct{})
	compactor := &allowlistCompactor{allowed: map[string]bool{switchExperimentMid: true, switchExperimentHigh: true}, summary: gatedSummary{release: release}}
	done := make(chan struct{}, 8)
	x := newPreparingExperiment(compactor, time.Minute, func(parent context.Context, timeout time.Duration, fn func(context.Context)) {
		go func() {
			runInline(parent, timeout, fn)
			done <- struct{}{}
		}()
	})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	applyExperimentTurn(x, largeAgentTurn(t, 10, prompt), switchExperimentMid, now)
	unready := largeAgentTurn(t, 11, prompt)
	resent := unready.MessageCount()
	started := time.Now()
	res := applyExperimentTurn(x, unready, switchExperimentMid, now.Add(time.Minute))
	assert.Less(t, time.Since(started), time.Second, "an unfinished summary never blocks the turn")
	assert.Equal(t, resent, unready.MessageCount(), "without a ready summary the turn keeps its full history")
	assert.NotEqual(t, "switch_experiment_"+string(routingpolicy.SwitchReasonCompactThenSwitch), res.PinTier)

	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("summary preparation never finished")
	}

	ready := largeAgentTurn(t, 12, prompt)
	resent = ready.MessageCount()
	res = applyExperimentTurn(x, ready, switchExperimentHigh, now.Add(2*time.Minute))
	assert.Equal(t, "switch_experiment_"+string(routingpolicy.SwitchReasonCompactThenSwitch), res.PinTier)
	assert.Equal(t, switchExperimentHigh, res.Decision.Model)
	assert.Less(t, ready.MessageCount(), resent, "the prepared summary is applied when the switch happens")
	assert.True(t, firstMessageIsSummary(t, ready))
}

func TestSwitchExperiment_SummaryTimeoutFallsBackToFullHistory(t *testing.T) {
	const prompt = "fix the parser bug; never edit test files"
	compactor := &allowlistCompactor{allowed: map[string]bool{switchExperimentMid: true, switchExperimentHigh: true}, summary: gatedSummary{release: make(chan struct{})}}
	x := newPreparingExperiment(compactor, 20*time.Millisecond, runInline)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	applyExperimentTurn(x, largeAgentTurn(t, 10, prompt), switchExperimentMid, now)
	turn := largeAgentTurn(t, 11, prompt)
	resent := turn.MessageCount()
	started := time.Now()
	res := applyExperimentTurn(x, turn, switchExperimentHigh, now.Add(time.Minute))
	assert.Less(t, time.Since(started), time.Second)
	assert.Equal(t, resent, turn.MessageCount(), "a timed-out summary leaves the full history")
	assert.False(t, firstMessageIsSummary(t, turn))
	assert.NotEqual(t, "switch_experiment_"+string(routingpolicy.SwitchReasonCompactThenSwitch), res.PinTier)
	_, stored := x.compactions.Load(hex.EncodeToString(append([]byte{7}, make([]byte, sessionpin.SessionKeyLen-1)...)), now.Add(time.Minute))
	assert.False(t, stored, "a failed summary is never stored")
}

func TestSwitchExperiment_SummarizerPrefersAWarmAllowedModel(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	x := newPreparingExperiment(&allowlistCompactor{allowed: map[string]bool{switchExperimentMid: true, switchExperimentHigh: true}}, time.Second, runInline)
	warmHigh := switchExperimentThread{caches: sessionpin.ModelCaches{
		switchExperimentLow:  {ServedAt: now},
		switchExperimentHigh: {ServedAt: now.Add(-time.Minute)},
		switchExperimentMid:  {ServedAt: now.Add(-10 * time.Minute)},
	}}
	assert.Equal(t, switchExperimentHigh, x.summarizerFor(switchExperimentLow, warmHigh, false, now), "the warm allowed model beats a cold one")
	assert.Equal(t, switchExperimentMid, x.summarizerFor(switchExperimentMid, warmHigh, false, now), "an allowed current model summarizes on its own cache")
	coldOnly := switchExperimentThread{caches: sessionpin.ModelCaches{switchExperimentLow: {ServedAt: now}}}
	assert.Equal(t, switchExperimentMid, x.summarizerFor(switchExperimentLow, coldOnly, false, now), "with no warm allowed model the reviewed default summarizes")
}

var preloadedPrompt = "fix the parser bug; never edit test files\n" + strings.Repeat("def handler(request): return parse(request)  # preloaded source\n", 14_000)

func newFlooredExperiment(compactor ThreadCompactor, spawn func(context.Context, time.Duration, func(context.Context))) *switchExperiment {
	x := newPreparingExperiment(compactor, time.Minute, spawn)
	x.cfg.Tuning.CompactMinPromptTokens = 150_000
	return x
}

func TestSwitchExperiment_PreparesAsSoonAsThePromptCrossesTheFloor(t *testing.T) {
	compactor := &allowlistCompactor{allowed: map[string]bool{switchExperimentMid: true, switchExperimentHigh: true}, summary: fixedSummary("SUMMARY")}
	x := newFlooredExperiment(compactor, runInline)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	applyExperimentTurn(x, largeAgentTurn(t, 1, preloadedPrompt), switchExperimentMid, now)
	applyExperimentTurn(x, largeAgentTurn(t, 2, preloadedPrompt), switchExperimentMid, now.Add(10*time.Second))
	require.Len(t, compactor.summarizers(), 1, "the summary starts on the first turn that can be planned above the floor, before any switch")

	switchTurn := largeAgentTurn(t, 3, preloadedPrompt)
	resent := switchTurn.MessageCount()
	res := applyExperimentTurn(x, switchTurn, switchExperimentHigh, now.Add(20*time.Second))
	assert.Equal(t, "switch_experiment_"+string(routingpolicy.SwitchReasonCompactThenSwitch), res.PinTier)
	assert.Less(t, switchTurn.MessageCount(), resent, "the early summary is applied once the tail is long enough")
	assert.Len(t, compactor.summarizers(), 1)
}

func TestSwitchExperiment_KeepsThePreparedSummaryCurrent(t *testing.T) {
	compactor := &allowlistCompactor{allowed: map[string]bool{switchExperimentMid: true, switchExperimentHigh: true}, summary: fixedSummary("SUMMARY")}
	x := newFlooredExperiment(compactor, runInline)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	for rounds := 1; rounds <= 8; rounds++ {
		applyExperimentTurn(x, largeAgentTurn(t, rounds, preloadedPrompt), switchExperimentMid, now.Add(time.Duration(rounds)*10*time.Second))
	}
	assert.Len(t, compactor.summarizers(), 2, "the summary is re-prepared once the unsummarized tail grows well past the configured tail")
}

func notReadySwitchTurn(t *testing.T, upgradeHorizon int) (turnLoopResult, *translate.RequestEnvelope, int) {
	t.Helper()
	compactor := &allowlistCompactor{allowed: map[string]bool{switchExperimentMid: true, switchExperimentHigh: true}, summary: gatedSummary{release: make(chan struct{})}}
	x := newFlooredExperiment(compactor, func(context.Context, time.Duration, func(context.Context)) {})
	x.cfg.Tuning.UpgradeHorizonTurns = upgradeHorizon
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	applyExperimentTurn(x, largeAgentTurn(t, 10, preloadedPrompt), switchExperimentMid, now)
	turn := largeAgentTurn(t, 11, preloadedPrompt)
	resent := turn.MessageCount()
	res := applyExperimentTurn(x, turn, switchExperimentHigh, now.Add(time.Minute))
	return res, turn, resent
}

func TestSwitchExperiment_StaysWhenTheSummaryIsNotReadyAndTheFullRewriteDoesNotPay(t *testing.T) {
	res, turn, resent := notReadySwitchTurn(t, 25)
	assert.Equal(t, switchExperimentMid, res.Decision.Model, "without a summary the switch only happens when the no-compaction rule would make it")
	assert.Equal(t, "switch_experiment_"+string(routingpolicy.SwitchReasonUpgradePending), res.PinTier)
	assert.Equal(t, resent, turn.MessageCount())
	assert.False(t, firstMessageIsSummary(t, turn))
}

func TestSwitchExperiment_SwitchesWithFullHistoryWhenTheSummaryIsNotReadyAndTheFullRewritePays(t *testing.T) {
	res, turn, resent := notReadySwitchTurn(t, 10_000)
	assert.Equal(t, switchExperimentHigh, res.Decision.Model)
	assert.Equal(t, "switch_experiment_"+string(routingpolicy.SwitchReasonSummaryNotReady), res.PinTier)
	assert.Equal(t, resent, turn.MessageCount(), "the switch carries the full history")
	assert.False(t, firstMessageIsSummary(t, turn))
}

func TestSwitchExperiment_CapsTheSummaryOutput(t *testing.T) {
	compactor := &allowlistCompactor{allowed: map[string]bool{switchExperimentMid: true, switchExperimentHigh: true}, summary: fixedSummary("SUMMARY")}
	x := newFlooredExperiment(compactor, runInline)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	applyExperimentTurn(x, largeAgentTurn(t, 10, preloadedPrompt), switchExperimentMid, now)
	applyExperimentTurn(x, largeAgentTurn(t, 11, preloadedPrompt), switchExperimentHigh, now.Add(time.Minute))
	require.NotEmpty(t, compactor.summaryTokens)
	assert.Equal(t, DefaultSwitchCompactionSummaryTokens, compactor.summaryTokens[0])
	assert.Equal(t, 1_500, DefaultSwitchCompactionSummaryTokens)
}
