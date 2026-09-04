package timing_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"weave-os/router/internal/timing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// First output must be later than first byte on a reasoning-stall stream.
func TestFirstOutputMark_MeasuresOutputNotFirstByte(t *testing.T) {
	ctx, tm := timing.WithTiming(context.Background())

	tm.StampUpstreamRequest()
	tm.StampUpstreamFirstByte()
	time.Sleep(20 * time.Millisecond)
	timing.FirstOutputMark(ctx, func() {})()

	ttfb := tm.Ms(&tm.UpstreamRequestNanos, &tm.UpstreamFirstByteNanos)
	firstOutput := tm.Ms(&tm.UpstreamRequestNanos, &tm.UpstreamFirstOutputNanos)

	assert.Greater(t, firstOutput, ttfb,
		"first output must be measured from the output frame, not the envelope")
	assert.GreaterOrEqual(t, firstOutput, int64(20))
}

// The mark feeds the output-stall watchdog; stamping must not swallow it.
func TestFirstOutputMark_StillCallsWrappedMark(t *testing.T) {
	var calls atomic.Int64
	ctx, _ := timing.WithTiming(context.Background())
	mark := timing.FirstOutputMark(ctx, func() { calls.Add(1) })

	mark()
	mark()
	mark()

	assert.Equal(t, int64(3), calls.Load(), "every output frame must still reach the watchdog")
}

// Later frames must not move the timestamp — it is a first-output measurement.
func TestFirstOutputMark_StampsOnlyOnce(t *testing.T) {
	ctx, tm := timing.WithTiming(context.Background())
	mark := timing.FirstOutputMark(ctx, func() {})

	mark()
	first := tm.UpstreamFirstOutputNanos.Load()
	require.NotZero(t, first)
	time.Sleep(5 * time.Millisecond)
	mark()

	assert.Equal(t, first, tm.UpstreamFirstOutputNanos.Load(), "only the first output frame counts")
}

// A stream that never produces output must leave the field unset rather than
// reporting 0 ms, which would read as "instant" on a dashboard.
func TestFirstOutputMark_UnsetWhenNoOutputEverArrives(t *testing.T) {
	_, tm := timing.WithTiming(context.Background())
	tm.StampUpstreamRequest()

	assert.Zero(t, tm.Ms(&tm.UpstreamRequestNanos, &tm.UpstreamFirstOutputNanos),
		"no output frame means no measurement")
}

// Providers build the mark from the request context, which has no Timing on
// non-proxied paths.
func TestFirstOutputMark_NilTimingIsSafe(t *testing.T) {
	var calls int
	mark := timing.FirstOutputMark(context.Background(), func() { calls++ })

	assert.NotPanics(t, mark, "a context without Timing must not panic the watchdog mark")
	assert.Equal(t, 1, calls)
}
