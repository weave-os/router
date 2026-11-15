package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
)

// turnAdmissionServices is what the shared turn preamble reaches on the proxy
// service.
type turnAdmissionServices interface {
	withClassifierInput(ctx context.Context, body []byte, endpoint router.TranslationEndpoint) (context.Context, error)
	effectiveCaptureMode(ctx context.Context) ContentCaptureMode
	checkUserMonthlySpendLimit(ctx context.Context, headers http.Header, routePath string) (context.Context, error)
	withUsageObserver(ctx context.Context, headers http.Header) context.Context
	withRateLimitTurn(ctx context.Context) (context.Context, *dispatch.ThrottleLedger)
	newTelemetryRecorder() telemetry.Recorder
}

var _ turnAdmissionServices = (*Service)(nil)

// withTurnInput must see the body before the entrypoint rewrites it: the
// attempt trace compares later dispatches against these exact bytes.
func withTurnInput(ctx context.Context, s turnAdmissionServices, body []byte, headers http.Header, endpoint router.TranslationEndpoint) (context.Context, error) {
	ctx = withAttemptTrace(ctx, body, headers)
	ctx, err := s.withClassifierInput(ctx, body, endpoint)
	if err != nil {
		return ctx, err
	}
	return requestcontext.WithContentLogging(ctx, s.effectiveCaptureMode(ctx) != CaptureOff), nil
}

type admittedTurn struct {
	rateLimit *dispatch.ThrottleLedger
	facts     requestcontext.RequestFacts
	start     time.Time
}

// admitTurn rejects an over-cap user before any per-turn state is opened.
func admitTurn(ctx context.Context, s turnAdmissionServices, r *http.Request) (context.Context, admittedTurn, error) {
	ctx, err := s.checkUserMonthlySpendLimit(ctx, r.Header, r.URL.Path)
	if err != nil {
		return ctx, admittedTurn{}, err
	}
	ctx = s.withUsageObserver(ctx, r.Header)
	ctx = withTurnFailures(ctx)
	ctx, rateLimit := s.withRateLimitTurn(ctx)
	turn := admittedTurn{rateLimit: rateLimit, start: time.Now(), facts: requestFactsFrom(ctx, r.Header)}
	return telemetry.WithRecorder(ctx, s.newTelemetryRecorder()), turn, nil
}
