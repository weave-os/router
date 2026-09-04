package proxy

import (
	"context"
	"testing"

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
