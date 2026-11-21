package dispatch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
)

func TestRescueConfigEarlyStopRedoFallsBackToOffOnInvalidMode(t *testing.T) {
	ctx := context.Background()

	assert.Equal(t, flags.EarlyStopRedoOff, dispatch.RescueConfig{EarlyStopRedo: "bogus"}.EarlyStopRedoMode(ctx))
	assert.Equal(t, flags.EarlyStopRedoOff, dispatch.RescueConfig{EarlyStopRedo: flags.EarlyStopRedoOff}.EarlyStopRedoMode(ctx))
}
