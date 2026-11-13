package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSleepWithContext(t *testing.T) {
	assert.NoError(t, sleepWithContext(context.Background(), time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, sleepWithContext(ctx, time.Hour), context.Canceled)
}
