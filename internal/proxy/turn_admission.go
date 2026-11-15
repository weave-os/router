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

// withTurnInput records what a native turn's routing and logging read from
// the inbound body: the attempt-trace baseline, the classifier input and
// whether content may be logged.
func withTurnInput(ctx context.Context, s *Service, body []byte, headers http.Header, endpoint router.TranslationEndpoint) (context.Context, error) {
	ctx = withAttemptTrace(ctx, body, headers)
	ctx, err := s.withClassifierInput(ctx, body, endpoint)
	if err != nil {
		return ctx, err
	}
	return requestcontext.WithContentLogging(ctx, s.effectiveCaptureMode(ctx) != CaptureOff), nil
}

// admittedTurn is what admitTurn opened for the turn.
type admittedTurn struct {
	rateLimit *dispatch.ThrottleLedger
	facts     requestcontext.RequestFacts
	start     time.Time
}

// admitTurn applies the per-user spend cap, then opens the turn's shared
// state: subscription usage observation, the failed-model and rate-limit
// ledgers, the caller facts and the telemetry recorder.
func admitTurn(ctx context.Context, s *Service, r *http.Request) (context.Context, admittedTurn, error) {
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
