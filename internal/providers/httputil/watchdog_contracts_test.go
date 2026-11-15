package httputil

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/timing"
)

const awsIdleConnectionReaper = 350 * time.Second

func TestByteIdleWatchdogsFireBeforeTheAWSIdleConnectionReaper(t *testing.T) {
	assert.Less(t, DefaultSSEIdleTimeout, awsIdleConnectionReaper)
	assert.Less(t, DefaultResponsesSSEIdleTimeout, awsIdleConnectionReaper)
}

func TestIntFromEnvLetsOperatorsDisableCountGuardsWithZero(t *testing.T) {
	t.Setenv("ROUTER_TEST_COUNT_GUARD", "0")
	assert.Equal(t, 0, intFromEnv("ROUTER_TEST_COUNT_GUARD", 8))
	t.Setenv("ROUTER_TEST_COUNT_GUARD", "-1")
	assert.Equal(t, -1, intFromEnv("ROUTER_TEST_COUNT_GUARD", 8))
	t.Setenv("ROUTER_TEST_COUNT_GUARD", "eight")
	assert.Equal(t, 8, intFromEnv("ROUTER_TEST_COUNT_GUARD", 8))
	t.Setenv("ROUTER_TEST_COUNT_GUARD", "0")
	assert.Equal(t, 45*time.Second, idleTimeoutFromEnv("ROUTER_TEST_COUNT_GUARD", 45*time.Second))
}

func TestLogUpstreamStatusLogsRateLimitsAtWarnAndOtherFailuresAtError(t *testing.T) {
	levels := map[int]string{
		http.StatusTooManyRequests:     "level=WARN",
		http.StatusBadRequest:          "level=ERROR",
		http.StatusInternalServerError: "level=ERROR",
	}
	for status, want := range levels {
		var buf strings.Builder
		ctx := observability.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)))
		LogUpstreamStatus(ctx, "upstream failed", status)
		assert.Contains(t, buf.String(), want, status)
	}
}

type causeCancellingReader struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	cause  error
}

func (r causeCancellingReader) Read([]byte) (int, error) {
	r.cancel(r.cause)
	<-r.ctx.Done()
	return 0, context.Canceled
}

func TestStreamBodySurfacesAnotherWatchdogsCauseInsteadOfCancellation(t *testing.T) {
	for _, cause := range []error{ErrUpstreamOutputStall, ErrUpstreamSlowThroughput} {
		ctx, cancel := context.WithCancelCause(context.Background())
		reader := causeCancellingReader{ctx: ctx, cancel: cancel, cause: cause}
		err := StreamBody(ctx, cancel, 0, reader, http.StatusOK, httptest.NewRecorder(), &timing.Timing{})
		require.ErrorIs(t, err, cause)
		assert.NotErrorIs(t, err, context.Canceled)
	}
}
