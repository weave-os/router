package proxy

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"
)

// newLoopSidewaysSvc wires the pin store, roster, and event store that handleToolCallLoopSideways touches.
func newLoopSidewaysSvc(pins *stubPinStore, events *recordingLoopStore, clusters map[string][]string) *Service {
	return NewService(nil, nil, nil, false, nil, pins, false, "anthropic", "claude-haiku-4-5", nil).
		WithLoopEscalationConfig(true, 0).
		WithLoopEscalationStore(events).
		WithStruggleEscalationRoster(NewStruggleRoster(fakeRosterSource{clusters: clusters}))
}

var loopSidewaysClusters = map[string][]string{
	"balanced": {"anthropic/claude-haiku-4.5", "anthropic/claude-sonnet-4-5"},
	"high":     {"anthropic/claude-opus-5"},
}

func loopSidewaysPin(model, reason string) sessionpin.Pin {
	return sessionpin.Pin{
		Model:       model,
		Provider:    "openai",
		PolicyGroup: "balanced",
		Reason:      reason,
	}
}

func TestHandleToolCallLoopSideways_RePinsOntoAnotherArm(t *testing.T) {
	pins := newStubPinStore()
	pins.getPin, pins.getFound = loopSidewaysPin("claude-haiku-4-5", "hmm_sticky"), true
	events := &recordingLoopStore{}
	svc := newLoopSidewaysSvc(pins, events, loopSidewaysClusters)

	res := svc.handleToolCallLoopSideways(context.Background(), loopTestSig, 5, uuid.New(), loopTestKey(1), "default")

	assert.True(t, res.Moved, "a loop with a dispatchable target must be rescued, not stopped")
	require.Len(t, pins.upserts, 1, "the rescue must land a pin so this same turn dispatches elsewhere")
	pin := pins.upserts[0]
	assert.Equal(t, "claude-opus-5", pin.Model)
	assert.Equal(t, translate.ReasonLoopSideways, pin.Reason)
	assert.Equal(t, "high", pin.PolicyGroup)
	assert.NotEmpty(t, pin.Provider, "the pin must name the provider that serves the target")

	require.Len(t, events.events, 1)
	assert.Equal(t, "claude-haiku-4-5", events.events[0].LoopingModel, "telemetry names the served model, not the client baseline")
	assert.Equal(t, "claude-opus-5", events.events[0].EscalationTarget)
}

func TestHandleToolCallLoopSideways_SkipsEffortVariantsOfTheLoopingModel(t *testing.T) {
	pins := newStubPinStore()
	pins.getPin, pins.getFound = loopSidewaysPin("gpt-5.6-luna", "hmm_sticky"), true
	svc := newLoopSidewaysSvc(pins, &recordingLoopStore{}, map[string][]string{
		"balanced": {"openai/gpt-5.6-luna"},
		"high":     {"openai/gpt-5.6-luna-pro", "anthropic/claude-opus-5"},
	})

	res := svc.handleToolCallLoopSideways(context.Background(), loopTestSig, 5, uuid.New(), loopTestKey(8), "default")

	assert.True(t, res.Moved)
	require.Len(t, pins.upserts, 1)
	assert.Equal(t, "claude-opus-5", pins.upserts[0].Model,
		"more effort on the same engine repeats the same tool call — the rescue must change model")
}

func TestSameEngine(t *testing.T) {
	cases := []struct {
		current, candidate string
		want               bool
	}{
		{"gpt-5.6-luna", "gpt-5.6-luna", true},
		{"gpt-5.6-luna", "gpt-5.6-luna-pro", true},
		{"gpt-5.6-luna", "gpt-5.6-luna:high", true},
		{"gpt-5.6-luna", "gpt-5.6-terra", false},
		{"gpt-5.6-luna", "gpt-5.6-sol-pro", false},
		{"gpt-5.4", "gpt-5.4-mini", false},
		{"claude-opus-5", "claude-opus-4-5", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sameEngine(c.current, c.candidate), "sameEngine(%q, %q)", c.current, c.candidate)
	}
}

func TestHandleToolCallLoopSideways_LeavesUserForcedPinAlone(t *testing.T) {
	pins := newStubPinStore()
	pins.getPin, pins.getFound = loopSidewaysPin("claude-haiku-4-5", translate.ReasonUserForceModel), true
	svc := newLoopSidewaysSvc(pins, &recordingLoopStore{}, loopSidewaysClusters)

	res := svc.handleToolCallLoopSideways(context.Background(), loopTestSig, 5, uuid.New(), loopTestKey(2), "default")

	assert.False(t, res.Moved)
	assert.True(t, res.UserForced, "the caller must skip pin eviction for a /force-model session")
	assert.Empty(t, pins.upserts, "an explicit force-model pin outranks the automatic move")
}

func TestHandleToolCallLoopSideways_DoesNotRescueTwice(t *testing.T) {
	for _, reason := range []string{translate.ReasonLoopSideways, translate.ReasonLoopEscalation, translate.ReasonStruggleEscalation} {
		t.Run(reason, func(t *testing.T) {
			pins := newStubPinStore()
			pins.getPin, pins.getFound = loopSidewaysPin("claude-haiku-4-5", reason), true
			svc := newLoopSidewaysSvc(pins, &recordingLoopStore{}, loopSidewaysClusters)

			res := svc.handleToolCallLoopSideways(context.Background(), loopTestSig, 5, uuid.New(), loopTestKey(3), "default")

			assert.False(t, res.Moved, "looping again after a rescue is a task problem; stop the turn")
			assert.False(t, res.UserForced)
			assert.Empty(t, pins.upserts)
		})
	}
}

func TestHandleToolCallLoopSideways_NoPinMeansNoAttribution(t *testing.T) {
	pins := newStubPinStore()
	svc := newLoopSidewaysSvc(pins, &recordingLoopStore{}, loopSidewaysClusters)

	res := svc.handleToolCallLoopSideways(context.Background(), loopTestSig, 5, uuid.New(), loopTestKey(4), "default")

	assert.False(t, res.Moved)
	assert.Empty(t, res.LoopingModel, "with no pin the caller falls back to the requested model")
}

func TestHandleToolCallLoopSideways_NoDispatchableTargetFallsBack(t *testing.T) {
	pins := newStubPinStore()
	pins.getPin, pins.getFound = loopSidewaysPin("claude-opus-5", "hmm_sticky"), true
	svc := newLoopSidewaysSvc(pins, &recordingLoopStore{}, map[string][]string{"balanced": {"anthropic/claude-opus-5"}})

	res := svc.handleToolCallLoopSideways(context.Background(), loopTestSig, 5, uuid.New(), loopTestKey(5), "default")

	assert.False(t, res.Moved, "the only arm in the roster is the one looping")
	assert.Equal(t, "claude-opus-5", res.LoopingModel, "the fallback stop must still be attributed to the pin")
	assert.Equal(t, "openai", res.LoopingProvider)
	assert.Empty(t, pins.upserts)
}

func TestHandleToolCallLoopSideways_DisabledFallsBack(t *testing.T) {
	pins := newStubPinStore()
	pins.getPin, pins.getFound = loopSidewaysPin("claude-haiku-4-5", "hmm_sticky"), true
	svc := newLoopSidewaysSvc(pins, &recordingLoopStore{}, loopSidewaysClusters).
		WithLoopEscalationConfig(false, 0)

	res := svc.handleToolCallLoopSideways(context.Background(), loopTestSig, 5, uuid.New(), loopTestKey(6), "default")

	assert.False(t, res.Moved)
	assert.Empty(t, pins.upserts, "the kill switch must stop the pin write, not just the logging")
}

func TestHandleToolCallLoopSideways_PinWriteFailureFallsBack(t *testing.T) {
	pins := newStubPinStore()
	pins.getPin, pins.getFound = loopSidewaysPin("claude-haiku-4-5", "hmm_sticky"), true
	pins.upsertErr = assert.AnError
	events := &recordingLoopStore{}
	svc := newLoopSidewaysSvc(pins, events, loopSidewaysClusters)

	res := svc.handleToolCallLoopSideways(context.Background(), loopTestSig, 5, uuid.New(), loopTestKey(7), "default")

	assert.False(t, res.Moved, "an unwritten pin means the turn would dispatch back onto the looping model")
	assert.Empty(t, events.events, "no rescue happened, so no rescue event")
}

func TestLoopAttribution_PrefersThePin(t *testing.T) {
	model, provider := loopAttribution("gpt-5.6-luna", "openai", "claude-fable-5", "anthropic")
	assert.Equal(t, "gpt-5.6-luna", model)
	assert.Equal(t, "openai", provider)

	model, provider = loopAttribution("", "", "claude-fable-5", "anthropic")
	assert.Equal(t, "claude-fable-5", model, "with no pin the requested model is all we know")
	assert.Equal(t, "anthropic", provider)
}

func TestDetectToolCallLoop_PollingToolsDoNotTrip(t *testing.T) {
	for name := range pollToolNames {
		t.Run(name, func(t *testing.T) {
			calls := make([]toolCall, 0, 8)
			for range 8 {
				calls = append(calls, toolCall{name: name, input: map[string]any{"shell_id": "abc"}})
			}
			env, err := translate.ParseAnthropic(buildBodyWithToolCalls(t, calls))
			require.NoError(t, err)

			loop, _, _ := detectToolCallLoop(env)
			assert.False(t, loop, "draining a background shell returns new output on every identical call")
		})
	}
}

func TestDetectToolCallLoop_PollingDoesNotMaskARealLoop(t *testing.T) {
	// A genuine repeat interleaved with polls still trips: the exemption drops
	// poll signatures from the counts, not the whole window.
	calls := []toolCall{}
	for range 5 {
		calls = append(calls,
			toolCall{name: "shell_output", input: map[string]any{"shell_id": "abc"}},
			toolCall{name: "ls", input: map[string]any{"path": "/tmp"}},
		)
	}
	env, err := translate.ParseAnthropic(buildBodyWithToolCalls(t, calls))
	require.NoError(t, err)

	loop, sig, _ := detectToolCallLoop(env)
	assert.True(t, loop)
	assert.Equal(t, "ls", sig.Name)
}
