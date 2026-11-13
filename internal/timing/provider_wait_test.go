package timing_test

import (
	"context"
	"testing"

	"weave-os/router/internal/timing"

	"github.com/stretchr/testify/assert"
)

func TestProviderWaitTracksAttemptsAndFirstOutput(t *testing.T) {
	var waiting []bool
	ctx := timing.WithProviderWaitObserver(context.Background(), func(active bool) {
		waiting = append(waiting, active)
	})
	finish := timing.BeginProviderWait(ctx)
	assert.Equal(t, []bool{true}, waiting)
	finish()
	finish = timing.BeginProviderWait(ctx)
	marks := 0
	timing.FirstOutputMark(ctx, func() { marks++ })()
	assert.Equal(t, []bool{true, false, true, false}, waiting)
	assert.Equal(t, 1, marks)
	finish()
	assert.False(t, waiting[len(waiting)-1])
}

func TestProviderWaitWithoutObserver(t *testing.T) {
	assert.NotPanics(t, func() {
		timing.BeginProviderWait(context.Background())()
		timing.FirstOutputMark(context.Background(), func() {})()
	})
}
