package proxy

import (
	"context"
	"net/http"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/capture"
)

type turnAdmissionServices interface {
	withClassifierInput(ctx context.Context, strategy router.Strategy, body []byte, endpoint router.TranslationEndpoint) (context.Context, error)
	effectiveCaptureMode(ctx context.Context) capture.Mode
	checkUserMonthlySpendLimit(ctx context.Context, headers http.Header, routePath string) (context.Context, error)
	withUsageObserver(ctx context.Context, headers http.Header) context.Context
	withRateLimitTurn(ctx context.Context) (context.Context, *dispatch.ThrottleLedger)
	newTelemetryRecorder() telemetry.Recorder
}

var _ turnAdmissionServices = (*Service)(nil)

func withTurnInput(ctx context.Context, s turnAdmissionServices, body []byte, headers http.Header, endpoint router.TranslationEndpoint) (context.Context, router.Strategy, error) {
	ctx = withAttemptTrace(ctx, body, headers)
	strategy := router.StrategyFromContext(ctx)
	ctx, err := s.withClassifierInput(ctx, strategy, body, endpoint)
	if err != nil {
		return ctx, strategy, err
	}
	return requestcontext.WithContentLogging(ctx, s.effectiveCaptureMode(ctx) != capture.Off), strategy, nil
}

type admittedTurn struct {
	rateLimit *dispatch.ThrottleLedger
	facts     requestcontext.RequestFacts
	start     time.Time
}

func admitTurn(ctx context.Context, s turnAdmissionServices, clock auth.Clock, r *http.Request) (context.Context, admittedTurn, error) {
	ctx, err := s.checkUserMonthlySpendLimit(ctx, r.Header, r.URL.Path)
	if err != nil {
		return ctx, admittedTurn{}, err
	}
	ctx = s.withUsageObserver(ctx, r.Header)
	ctx = requestcontext.WithTurnFailures(ctx)
	ctx, rateLimit := s.withRateLimitTurn(ctx)
	turn := admittedTurn{rateLimit: rateLimit, start: clock.Now(), facts: credentials.RequestFactsFrom(ctx, r.Header)}
	return telemetry.WithRecorder(ctx, s.newTelemetryRecorder()), turn, nil
}
