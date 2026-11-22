package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
)

type fakeTurnAdmission struct {
	spendErr error
	opened   []string
}

func (f *fakeTurnAdmission) withClassifierInput(ctx context.Context, _ router.Strategy, _ []byte, _ router.TranslationEndpoint) (context.Context, error) {
	return ctx, nil
}

func (f *fakeTurnAdmission) effectiveCaptureMode(context.Context) ContentCaptureMode {
	return CaptureOff
}

func (f *fakeTurnAdmission) checkUserMonthlySpendLimit(ctx context.Context, _ http.Header, _ string) (context.Context, error) {
	return ctx, f.spendErr
}

func (f *fakeTurnAdmission) withUsageObserver(ctx context.Context, _ http.Header) context.Context {
	f.opened = append(f.opened, "usage observer")
	return ctx
}

func (f *fakeTurnAdmission) withRateLimitTurn(ctx context.Context) (context.Context, *dispatch.ThrottleLedger) {
	f.opened = append(f.opened, "rate limit")
	return ctx, nil
}

func (f *fakeTurnAdmission) newTelemetryRecorder() telemetry.Recorder {
	f.opened = append(f.opened, "telemetry")
	return nil
}

func TestAdmitTurnRejectsOverCapUserBeforeOpeningTurnState(t *testing.T) {
	capErr := errors.New("monthly spend limit reached")
	fake := &fakeTurnAdmission{spendErr: capErr}
	ctx, _, err := admitTurn(context.Background(), fake, nil, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", nil))
	require.ErrorIs(t, err, capErr)
	assert.Empty(t, fake.opened)
	assert.Nil(t, turnFailuresFromContext(ctx))
}

func TestAdmitTurnOpensTurnState(t *testing.T) {
	fake := &fakeTurnAdmission{}
	admittedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock := auth.Clock(func() time.Time { return admittedAt })
	ctx, turn, err := admitTurn(context.Background(), fake, clock, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", nil))
	require.NoError(t, err)
	assert.Equal(t, []string{"usage observer", "rate limit", "telemetry"}, fake.opened)
	assert.NotNil(t, turnFailuresFromContext(ctx))
	assert.NotEmpty(t, turn.facts.RequestID)
	assert.Equal(t, admittedAt, turn.start)
}
