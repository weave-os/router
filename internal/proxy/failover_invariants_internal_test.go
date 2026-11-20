package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type cancelRejectingDemotionStore struct {
	*demotionStubPinStore
}

func (s cancelRejectingDemotionStore) ExpireAndDemoteModel(ctx context.Context, expired sessionpin.Pin, model string, reason sessionpin.DemotionReason) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.demotionStubPinStore.ExpireAndDemoteModel(ctx, expired, model, reason)
}

type cancelRejectingEvictionStore struct {
	*evictionStubPinStore
}

func (s cancelRejectingEvictionStore) Upsert(ctx context.Context, p sessionpin.Pin) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.evictionStubPinStore.Upsert(ctx, p)
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestIsCommittedStreamFailure_WatchdogCauseBeatsWrappedCancellation(t *testing.T) {
	for _, sentinel := range []error{providers.ErrUpstreamIdleTimeout, providers.ErrUpstreamOutputStall, providers.ErrUpstreamSlowThroughput} {
		stalled, cancel := context.WithCancelCause(context.Background())
		cancel(sentinel)
		assert.True(t, isCommittedStreamFailure(stalled, fmt.Errorf("stream: %w", context.Canceled)), sentinel.Error())
	}
}

func TestCommittedStreamStrikeLandsAfterClientCancel(t *testing.T) {
	stub := &demotionStubPinStore{}
	svc := newDemotionTestService(cancelRejectingDemotionStore{stub}, true)

	demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
		canceledContext(),
		true,
		false,
		fmt.Errorf("stream: %w", providers.ErrUpstreamIdleTimeout),
		demotedArm,
		"hmm:authoritative model=claude-opus-4-7", sessionTarget{strategy: router.StrategyFromContext(canceledContext()), installationID: uuid.New(), sessionKey: nonZeroSessionKey()},

		sessionpin.DefaultRole, sessionpin.DefaultRole)

	assert.Equal(t, demotedArm, demoted)
	assert.NotEmpty(t, stub.demotions)
}

func TestPinEvictionSecondStrikeLandsAfterClientCancel(t *testing.T) {
	stub := &evictionStubPinStore{incrementNext: []int{2}}
	svc := newEvictionTestService(stub)
	svc.pinStore = cancelRejectingEvictionStore{stub}

	svc.maybeEvictPinAfterUpstreamErr(
		canceledContext(),
		true,
		&providers.UpstreamStatusError{Status: http.StatusBadRequest},
		"cluster:v0.57 model=gpt-5.5 provider=openai", sessionTarget{strategy: router.StrategyFromContext(canceledContext()), installationID: uuid.New(), sessionKey: nonZeroSessionKey()},

		sessionpin.DefaultRole)

	require.Len(t, stub.upserts, 1, "the second consecutive strike expires the pin even after the client left")
	assert.Empty(t, stub.upserts[0].Model)
}

func TestReplayedUpstreamErrorDropsBodyFraming(t *testing.T) {
	upstream := func() *providers.UpstreamErrorResponse {
		return &providers.UpstreamErrorResponse{
			Status: http.StatusBadRequest,
			Headers: http.Header{
				"Content-Length":   {"999"},
				"Content-Encoding": {"gzip"},
				"X-Request-Id":     {"req_synthetic"},
			},
			Body: []byte(`{"error":{"message":"bad tool"}}`),
		}
	}
	for name, flush := range map[string]func(http.ResponseWriter, error){
		"buffered": flushBufferedIfPresent,
		"openai":   flushUpstreamErrorAsOpenAI,
	} {
		rec := httptest.NewRecorder()
		flush(rec, upstream())
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Empty(t, rec.Header().Get("Content-Length"), name)
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), "%s forwards the upstream bytes, so their encoding stays", name)
		assert.Equal(t, "req_synthetic", rec.Header().Get("X-Request-Id"), name)
	}

	rec := httptest.NewRecorder()
	flushUpstreamErrorAsAnthropic(rec, upstream())
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Length"))
	assert.Empty(t, rec.Header().Get("Content-Encoding"), "a rewritten body drops the upstream encoding")
	assert.Equal(t, "req_synthetic", rec.Header().Get("X-Request-Id"))
}

func TestCyberRefusalGate_FlushHeldWhileWithholdingPreamble(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newCyberRefusalGate(rec, true)

	_, err := gate.Write([]byte(responsesCreatedFrame))
	require.NoError(t, err)
	gate.Flush()

	assert.False(t, rec.Flushed, "a flush must not push the withheld preamble's headers to the client")
	assert.Empty(t, rec.Body.String())
}

func TestCyberRefusalGate_ForwardsOutputProgressArming(t *testing.T) {
	armer := &armerRecorder{ResponseRecorder: httptest.NewRecorder()}
	gate := newCyberRefusalGate(armer, true)

	assert.True(t, gate.ArmOutputProgress(func() {}), "a withheld preamble must not disarm the stall watchdog")
	assert.Equal(t, 1, armer.marks)
	assert.False(t, newCyberRefusalGate(httptest.NewRecorder(), true).ArmOutputProgress(func() {}))
}

func TestEvictPinAfterDegenerateResponse_ForceModelSkipped(t *testing.T) {
	for _, reason := range []string{translate.ReasonUserForceModel, translate.ReasonUserForceModel + "+tier_clamp"} {
		store := &evictionStubPinStore{}
		newEvictionTestService(store).evictPinAfterDegenerateResponse(
			context.Background(), router.StrategyFromContext(context.Background()), true, reason, uuid.New(), nonZeroSessionKey(), sessionpin.DefaultRole)
		assert.Empty(t, store.upserts, reason)
	}

	store := &evictionStubPinStore{}
	newEvictionTestService(store).evictPinAfterDegenerateResponse(
		context.Background(), router.StrategyFromContext(context.Background()), true, "cluster:v0.57 model=gpt-5.5 provider=openai", uuid.New(), nonZeroSessionKey(), sessionpin.DefaultRole)
	assert.Len(t, store.upserts, 1, "an automatic pin is evicted")
}

type pairedPinStore struct {
	*demotionStubPinStore
	pin sessionpin.Pin
}

func (s pairedPinStore) Get(context.Context, [sessionpin.SessionKeyLen]byte, string) (sessionpin.Pin, bool, error) {
	return s.pin, true, nil
}

func TestCyberRefusalRetryTarget_NeverLandsOnRefusingVendor(t *testing.T) {
	ctx := context.Background()
	store := pairedPinStore{
		demotionStubPinStore: &demotionStubPinStore{},
		pin: sessionpin.Pin{
			PairedModel:    "claude-sonnet-5",
			PairedProvider: providers.ProviderAnthropic,
			Strategy:       router.StrategyFromContext(ctx),
		},
	}
	svc := withHMMPorts(NewService(nil,
		map[string]providers.Client{providers.ProviderOpenAI: openai.NewClient("test-openai-key", "http://127.0.0.1:1")},
		nil, store, false, providers.ProviderOpenAI, "gpt-5.5", nil)).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderOpenAI: {}}).
		WithCyberRefusalFallbackModel("gpt-5.5")

	_, found := svc.cyberRefusalRetryTarget(ctx,
		router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-sol"},
		sessionTarget{strategy: router.StrategyFromContext(ctx), sessionKey: nonZeroSessionKey()}, sessionpin.DefaultRole, 1000, 0, 0)

	assert.False(t, found, "with the off-vendor runner-up unreachable, the OpenAI fallback must not be used")
}
