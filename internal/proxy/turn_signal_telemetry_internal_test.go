package proxy

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnSignalCaptureAllowed(t *testing.T) {
	tests := []struct {
		name            string
		trainingAllowed bool
		capture         ContentCaptureMode
		want            bool
	}{
		{"full capture and training allowed", true, CaptureFull, true},
		{"hashed capture and training allowed", true, CaptureHashed, true},
		{"AI training opted out", false, CaptureFull, false},
		{"zero retention", true, CaptureOff, false},
		{"neither allowed", false, CaptureOff, false},
		{"unknown capture mode", true, ContentCaptureMode(99), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, turnSignalCaptureAllowed(tc.trainingAllowed, tc.capture))
		})
	}
}

func TestTurnSignalCaptureAllowed_FailsClosedOnDefaults(t *testing.T) {
	assert.False(t, turnSignalCaptureAllowed(false, ParseCaptureMode("")))
}

func sampleSignals() spiralSignals {
	return spiralSignals{
		errStats: translate.ToolResultErrorStats{
			Total:             23,
			Errored:           11,
			TrailingErrStreak: 4,
		},
		maxSameFileEdits:   7,
		sameFilePathHash:   "a1b2c3d4e5f60718",
		repeatFrac:         0.42,
		monologueLen:       3,
		toolCallCount:      31,
		messageCount:       64,
		pingPongLen:        8,
		stepsSinceProgress: 19,
		editAttempted:      true,
	}
}

func TestApplyTurnSignalTelemetry_CopiesFullSnapshot(t *testing.T) {
	var telemetry InsertTelemetryParams
	reasons := []spiralReason{spiralReasonErrStreak, spiralReasonPingPong}
	applyTurnSignalTelemetry(&telemetry, sampleSignals(), reasons, true, true, true, CaptureFull)

	assertInt32Ptr := func(field string, got *int32, want int32) {
		t.Helper()
		require.NotNil(t, got, field)
		assert.Equal(t, want, *got, field)
	}
	assertInt32Ptr("SpiralErrStreak", telemetry.SpiralErrStreak, 4)
	assertInt32Ptr("SpiralErroredResults", telemetry.SpiralErroredResults, 11)
	assertInt32Ptr("SpiralToolResults", telemetry.SpiralToolResults, 23)
	assertInt32Ptr("SpiralMaxSameFileEdits", telemetry.SpiralMaxSameFileEdits, 7)
	assertInt32Ptr("SpiralMonologueLen", telemetry.SpiralMonologueLen, 3)
	assertInt32Ptr("SpiralToolCallCount", telemetry.SpiralToolCallCount, 31)
	assertInt32Ptr("SpiralMessageCount", telemetry.SpiralMessageCount, 64)
	assertInt32Ptr("SpiralPingPongLen", telemetry.SpiralPingPongLen, 8)
	assertInt32Ptr("SpiralStepsSinceProgress", telemetry.SpiralStepsSinceProgress, 19)
	assert.Equal(t, "a1b2c3d4e5f60718", telemetry.SpiralSameFilePathHash)
	require.NotNil(t, telemetry.SpiralRepeatFrac)
	assert.Equal(t, 0.42, *telemetry.SpiralRepeatFrac)
	require.NotNil(t, telemetry.SpiralEditAttempted)
	assert.True(t, *telemetry.SpiralEditAttempted)
	assert.Equal(t, []string{string(spiralReasonErrStreak), string(spiralReasonPingPong)}, telemetry.SpiralReasons)
}

func TestApplyTurnSignalTelemetry_QuietTurnRecordsZeros(t *testing.T) {
	var telemetry InsertTelemetryParams
	applyTurnSignalTelemetry(&telemetry, spiralSignals{}, nil, true, true, true, CaptureFull)

	require.NotNil(t, telemetry.SpiralErrStreak)
	assert.Zero(t, *telemetry.SpiralErrStreak)
	require.NotNil(t, telemetry.SpiralToolCallCount)
	assert.Zero(t, *telemetry.SpiralToolCallCount)
	require.NotNil(t, telemetry.SpiralEditAttempted)
	assert.False(t, *telemetry.SpiralEditAttempted)
	require.NotNil(t, telemetry.SpiralReasons)
	assert.Empty(t, telemetry.SpiralReasons)
}

func TestApplyTurnSignalTelemetry_SkipsIneligibleTurns(t *testing.T) {
	tests := []struct {
		name            string
		computed        bool
		enabled         bool
		trainingAllowed bool
		capture         ContentCaptureMode
	}{
		{"AI training opted out", true, true, false, CaptureFull},
		{"zero retention", true, true, true, CaptureOff},
		{"capture disabled", true, false, true, CaptureFull},
		{"snapshot not computed", false, true, true, CaptureFull},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var telemetry InsertTelemetryParams
			applyTurnSignalTelemetry(&telemetry, sampleSignals(), []spiralReason{spiralReasonErrStreak},
				tc.computed, tc.enabled, tc.trainingAllowed, tc.capture)

			assert.Nil(t, telemetry.SpiralErrStreak)
			assert.Nil(t, telemetry.SpiralToolCallCount)
			assert.Nil(t, telemetry.SpiralRepeatFrac)
			assert.Nil(t, telemetry.SpiralEditAttempted)
			assert.Empty(t, telemetry.SpiralSameFilePathHash)
			assert.Nil(t, telemetry.SpiralReasons)
		})
	}
}

func TestApplyTurnSignalTelemetry_NilParamsIsNoOp(t *testing.T) {
	assert.NotPanics(t, func() {
		applyTurnSignalTelemetry(nil, sampleSignals(), nil, true, true, true, CaptureFull)
	})
}

type recordingSpiralStore struct {
	events []SpiralShadowEvent
}

func (s *recordingSpiralStore) InsertSpiralShadowEvent(_ context.Context, event SpiralShadowEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSpiralStore) CountSpiralShadowEvents(_ context.Context, _ []byte, _, _ string) (int64, error) {
	return 0, nil
}

func TestHandleSpiralShadow_RespectsPrivacySettings(t *testing.T) {
	tests := []struct {
		name            string
		trainingAllowed bool
		capture         ContentCaptureMode
		wantEvents      int
	}{
		{"allowed", true, CaptureFull, 1},
		{"AI training opted out", false, CaptureFull, 0},
		{"zero retention", true, CaptureOff, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordingSpiralStore{}
			service := &Service{spiralTracker: newSpiralTracker(), spiralShadowStore: store}
			var sessionKey [16]byte
			service.handleSpiralShadow(context.Background(), sampleSignals(),
				[]spiralReason{spiralReasonErrStreak}, uuid.New(), sessionKey,
				"default", "test-model", "tool_result", tc.trainingAllowed, tc.capture)
			assert.Len(t, store.events, tc.wantEvents)
		})
	}
}

// memorySessionTurnClock keeps the latest finish per session, like the
// Postgres upsert, and returns the finish it replaced.
type memorySessionTurnClock struct {
	finishes map[string]SessionTurnClockReading
}

func (c *memorySessionTurnClock) AdvanceSessionTurnClock(_ context.Context, advance SessionTurnClockAdvance) (SessionTurnClockReading, bool, error) {
	key := advance.InstallationID + string(advance.SessionKey)
	previous, found := c.finishes[key]
	if !found || previous.ResponseEndedAt.Before(advance.ResponseEndedAt) {
		c.finishes[key] = SessionTurnClockReading{ResponseEndedAt: advance.ResponseEndedAt, ServedModel: advance.ServedModel}
	}
	return previous, found, nil
}

func TestApplyUserPromptGap_TimesTypedPromptAgainstPreviousResponse(t *testing.T) {
	clock := &memorySessionTurnClock{finishes: map[string]SessionTurnClockReading{}}
	service := (&Service{}).WithSessionTurnClock(clock)
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	turn := func(offset time.Duration, latencyMs int64, turnType string, model string, typed bool) InsertTelemetryParams {
		return InsertTelemetryParams{
			InstallationID: "installation-1",
			SpanType:       "router.upstream",
			SessionKey:     []byte("session-a"),
			Timestamp:      start.Add(offset),
			TotalLatencyMs: latencyMs,
			TurnType:       turnType,
			DecisionModel:  model,
			OutputTokens:   120,
			UserPrompt:     &typed,
		}
	}
	apply := func(p InsertTelemetryParams) InsertTelemetryParams {
		service.applyUserPromptGap(context.Background(), slog.Default(), &p)
		return p
	}

	first := apply(turn(0, 4_000, "main_loop", "claude-opus-5-5", true))
	assert.Nil(t, first.UserPromptGapMs, "a session's first prompt has no previous response")

	toolTurn := apply(turn(5*time.Second, 3_000, "tool_result", "claude-sonnet-5", false))
	assert.Nil(t, toolTurn.UserPromptGapMs, "a tool result is not a typed prompt")

	classifier := apply(turn(9*time.Second, 500, "classifier", "claude-haiku-4-5", false))
	assert.Nil(t, classifier.UserPromptGapMs)

	// The tool turn finished at +8s; the classifier call beside it must not
	// move the clock, so the typed prompt at +20s waited 12s on the sonnet reply.
	typed := apply(turn(20*time.Second, 2_000, "main_loop", "claude-opus-5-5", true))
	require.NotNil(t, typed.UserPromptGapMs)
	assert.Equal(t, int64(12_000), *typed.UserPromptGapMs)
	assert.Equal(t, "claude-sonnet-5", typed.UserPromptGapPriorModel)
}

func TestApplyUserPromptGap_OverlappingPreviousResponseLeavesGapNull(t *testing.T) {
	clock := &memorySessionTurnClock{finishes: map[string]SessionTurnClockReading{}}
	service := (&Service{}).WithSessionTurnClock(clock)
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	typed := true
	long := InsertTelemetryParams{
		InstallationID: "installation-1", SpanType: "router.upstream", SessionKey: []byte("session-a"),
		Timestamp: start, TotalLatencyMs: 60_000, TurnType: "main_loop", DecisionModel: "gpt-6-sol", OutputTokens: 120, UserPrompt: &typed,
	}
	service.applyUserPromptGap(context.Background(), slog.Default(), &long)

	overlapping := long
	overlapping.Timestamp = start.Add(10 * time.Second)
	overlapping.TotalLatencyMs = 1_000
	service.applyUserPromptGap(context.Background(), slog.Default(), &overlapping)

	assert.Nil(t, overlapping.UserPromptGapMs, "a response still running when the prompt arrived is not a wait")
	assert.Empty(t, overlapping.UserPromptGapPriorModel)
}

func TestApplyUserPromptGap_FailedTurnDoesNotMoveTheClock(t *testing.T) {
	clock := &memorySessionTurnClock{finishes: map[string]SessionTurnClockReading{}}
	service := (&Service{}).WithSessionTurnClock(clock)
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	typed := true
	reply := InsertTelemetryParams{
		InstallationID: "installation-1", SpanType: "router.upstream", SessionKey: []byte("session-a"),
		Timestamp: start, TotalLatencyMs: 4_000, TurnType: "main_loop", DecisionModel: "claude-opus-5-5", OutputTokens: 120, UserPrompt: &typed,
	}
	service.applyUserPromptGap(context.Background(), slog.Default(), &reply)

	failed := reply
	failed.Timestamp = start.Add(10 * time.Second)
	failed.TotalLatencyMs = 1_000
	failed.DecisionModel = "gpt-6-sol"
	failed.OutputTokens = 0
	failed.UpstreamStatusCode = 502
	failed.ErrorClass = TurnErrorUpstream5xx
	service.applyUserPromptGap(context.Background(), slog.Default(), &failed)

	next := reply
	next.Timestamp = start.Add(20 * time.Second)
	service.applyUserPromptGap(context.Background(), slog.Default(), &next)

	// The 502 at +10s showed no reply, so the prompt at +20s waited 16s on opus.
	require.NotNil(t, next.UserPromptGapMs)
	assert.Equal(t, int64(16_000), *next.UserPromptGapMs)
	assert.Equal(t, "claude-opus-5-5", next.UserPromptGapPriorModel)
}

// deadlineRecordingClock records the deadline its advance ran under.
type deadlineRecordingClock struct {
	deadline time.Time
}

func (c *deadlineRecordingClock) AdvanceSessionTurnClock(ctx context.Context, _ SessionTurnClockAdvance) (SessionTurnClockReading, bool, error) {
	c.deadline, _ = ctx.Deadline()
	return SessionTurnClockReading{}, false, nil
}

func TestApplyUserPromptGap_ClockLeavesTheInsertItsBudget(t *testing.T) {
	clock := &deadlineRecordingClock{}
	service := (&Service{}).WithSessionTurnClock(clock)
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	row := InsertTelemetryParams{
		InstallationID: "installation-1", SpanType: "router.upstream", SessionKey: []byte("session-a"),
		Timestamp: time.Now(), TurnType: "main_loop", DecisionModel: "claude-opus-5-5", OutputTokens: 120,
	}
	service.applyUserPromptGap(parent, slog.Default(), &row)

	require.False(t, clock.deadline.IsZero(), "the clock must run under a deadline")
	assert.LessOrEqual(t, clock.deadline.Sub(parentDeadline), -3*time.Second,
		"the clock's budget must end well before the shared deadline the telemetry insert uses")
}
