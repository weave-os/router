package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/breaker"
)

// overloadAwareRouter picks the primary unless the request withdrew it from
// automatic selection, the way the scorer's soft filter does, and always
// offers the rescuer as a same-cluster candidate.
type overloadAwareRouter struct {
	routed        []string
	preferRescuer bool
}

func (r *overloadAwareRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	metadata := &router.RoutingMetadata{
		CandidateModels:    []string{rescuedPrimaryModel, rescuerModel},
		CandidateProviders: map[string]string{rescuerModel: providers.ProviderOpenAI},
	}
	decision := router.Decision{Provider: providers.ProviderAnthropic, Model: rescuedPrimaryModel, Reason: "hmm:authoritative model=" + rescuedPrimaryModel, Metadata: metadata}
	if _, withdrawn := req.AutomaticExcludedModels[rescuedPrimaryModel]; withdrawn || r.preferRescuer {
		decision = router.Decision{Provider: providers.ProviderOpenAI, Model: rescuerModel, Reason: "hmm:authoritative model=" + rescuerModel, Metadata: metadata}
	}
	r.routed = append(r.routed, decision.Model)
	return decision, nil
}

type overloadHarness struct {
	svc     *Service
	router  *overloadAwareRouter
	primary *switchableClient
	sibling *servingClient
	clock   time.Time
}

// switchableClient answers with failWith while it is set and otherwise serves
// a minimal Anthropic stream.
type switchableClient struct {
	failWith error
	calls    int
}

func (c *switchableClient) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.calls++
	if c.failWith != nil {
		return c.failWith
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, err := io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"model\":\""+rescuedPrimaryModel+"\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return err
}

func (*switchableClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func overloaded529() error {
	return &providers.UpstreamErrorResponse{Status: providerOverloadedStatus, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}
}

func newOverloadHarness(enabled bool) *overloadHarness {
	h := &overloadHarness{
		router:  &overloadAwareRouter{},
		primary: &switchableClient{failWith: overloaded529()},
		sibling: &servingClient{},
		clock:   time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	}
	now := func() time.Time { return h.clock }
	registry := breaker.New(breaker.Config{Strikes: 2, Window: time.Minute, RecoveryBase: 30 * time.Second, RecoveryCap: 2 * time.Minute}, now)
	h.svc = NewService(h.router, map[string]providers.Client{
		providers.ProviderAnthropic: h.primary,
		providers.ProviderOpenAI:    h.sibling,
	}, nil, false, nil, &demotionStubPinStore{}, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil }).
		WithProviderOverloaded(enabled).
		WithOverloadBreaker(registry)
	h.svc.now = now
	return h
}

func (h *overloadHarness) turn(t *testing.T, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	body := anthropicMessagesBody()
	err := h.svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
	require.NoError(t, err)
	return rec
}

func (h *overloadHarness) state() breaker.State {
	return h.svc.overloadBreaker.State(rescuedPrimaryModel)
}

// Two overloaded turns open the circuit; the next turn routes straight to the
// rescuer and never dispatches the overloaded model.
func TestOverloadBreaker_OpensAndRoutesAround(t *testing.T) {
	h := newOverloadHarness(true)

	h.turn(t, rescuedFailureCtx())
	assert.Equal(t, breaker.StateClosed, h.state())
	h.turn(t, rescuedFailureCtx())
	require.Equal(t, breaker.StateOpen, h.state())
	callsWhenOpened := h.primary.calls

	rec := h.turn(t, rescuedFailureCtx())

	assert.Equal(t, callsWhenOpened, h.primary.calls, "an open circuit keeps the model out of automatic selection")
	assert.Equal(t, rescuerModel, rec.Header().Get(HeaderRouterModel))
	assert.Equal(t, 3, h.sibling.calls)
}

// Each overloaded turn retries its binding in place; those retries are one
// strike, so a single bad turn cannot open the circuit by itself.
func TestOverloadBreaker_CountsTurnsNotAttempts(t *testing.T) {
	h := newOverloadHarness(true)

	h.turn(t, rescuedFailureCtx())

	assert.Greater(t, h.primary.calls, 1, "the turn retried the overloaded binding")
	assert.Equal(t, breaker.StateClosed, h.state())
}

// After the recovery timeout one request probes the model; a served probe
// closes the circuit and the model routes normally again.
func TestOverloadBreaker_HalfOpenProbeClosesOnSuccess(t *testing.T) {
	h := newOverloadHarness(true)
	h.turn(t, rescuedFailureCtx())
	h.turn(t, rescuedFailureCtx())
	require.Equal(t, breaker.StateOpen, h.state())

	h.clock = h.clock.Add(30 * time.Second)
	h.primary.failWith = nil
	rec := h.turn(t, rescuedFailureCtx())

	assert.Equal(t, rescuedPrimaryModel, rec.Header().Get(HeaderRouterModel), "the probe is routed to the recovered model")
	assert.Equal(t, breaker.StateClosed, h.state())
	assert.Equal(t, rescuedPrimaryModel, h.turn(t, rescuedFailureCtx()).Header().Get(HeaderRouterModel))
}

// A probe that hits another 529 is rescued as before and reopens the circuit
// for twice as long.
func TestOverloadBreaker_FailedProbeReopensWithBackoff(t *testing.T) {
	h := newOverloadHarness(true)
	h.turn(t, rescuedFailureCtx())
	h.turn(t, rescuedFailureCtx())

	h.clock = h.clock.Add(30 * time.Second)
	rec := h.turn(t, rescuedFailureCtx())

	assert.Equal(t, rescuerModel, rec.Header().Get(HeaderRouterModel), "the failed probe is still rescued")
	assert.Equal(t, breaker.StateOpen, h.state())
	h.clock = h.clock.Add(59 * time.Second)
	routedBefore := len(h.router.routed)
	h.turn(t, rescuedFailureCtx())
	assert.Equal(t, rescuerModel, h.router.routed[routedBefore], "the second open period is 60s, not 30s")
}

// A probe slot claimed by a request that ended up serving another model is
// released when the request ends, so the next request probes instead of
// waiting out the lease.
func TestOverloadBreaker_UnexercisedProbeIsReleased(t *testing.T) {
	h := newOverloadHarness(true)
	h.turn(t, rescuedFailureCtx())
	h.turn(t, rescuedFailureCtx())
	h.clock = h.clock.Add(30 * time.Second)

	h.router.preferRescuer = true
	rec := h.turn(t, rescuedFailureCtx())
	require.Equal(t, rescuerModel, rec.Header().Get(HeaderRouterModel))
	assert.Equal(t, breaker.StateOpen, h.state(), "the slot went back to open")

	h.router.preferRescuer = false
	h.primary.failWith = nil
	assert.Equal(t, rescuedPrimaryModel, h.turn(t, rescuedFailureCtx()).Header().Get(HeaderRouterModel))
	assert.Equal(t, breaker.StateClosed, h.state())
}

// Flag off, nothing is recorded and every turn pays the overload and rescue
// exactly as before.
func TestOverloadBreaker_FlagOffKeepsTodaysBehaviour(t *testing.T) {
	h := newOverloadHarness(false)

	for range 3 {
		h.turn(t, rescuedFailureCtx())
	}

	assert.Equal(t, []string{rescuedPrimaryModel, rescuedPrimaryModel, rescuedPrimaryModel}, h.router.routed)
	assert.Equal(t, breaker.StateClosed, h.state())
}

// A per-org override turns the breaker off for that org's requests even when
// the deployment default is on.
func TestOverloadBreaker_OrgOverrideDisables(t *testing.T) {
	h := newOverloadHarness(true)
	ctx := func() context.Context {
		return flags.WithOverrides(rescuedFailureCtx(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyProviderOverloaded: false}})
	}

	h.turn(t, ctx())
	h.turn(t, ctx())

	assert.Equal(t, breaker.StateClosed, h.state())
}

// An open circuit is a soft exclusion: the pin-drop log names why, and the
// reason is reported alongside operator disables.
func TestOverloadBreaker_ReportsReason(t *testing.T) {
	h := newOverloadHarness(true)
	h.turn(t, rescuedFailureCtx())
	h.turn(t, rescuedFailureCtx())

	reason, disabled := h.svc.globalAutomaticExclusionReason(context.Background(), rescuedPrimaryModel)

	assert.True(t, disabled)
	assert.Equal(t, overloadBreakerReason, reason)
}

// Committed streams, client cancellations, non-529 failures and BYOK gateways
// are not evidence of model overload.
func TestRecordOverloadOutcome_IgnoresNonOverloadSignals(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		committed bool
		err       error
	}{
		{name: "committed 529", provider: providers.ProviderAnthropic, committed: true, err: overloaded529()},
		{name: "502", provider: providers.ProviderAnthropic, err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}},
		{name: "429", provider: providers.ProviderAnthropic, err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}},
		{name: "client canceled", provider: providers.ProviderAnthropic, err: context.Canceled},
		{name: "gateway 529", provider: providers.ProviderAnthropicGateway, err: overloaded529()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOverloadHarness(true)
			for range 3 {
				h.svc.recordOverloadOutcome(context.Background(), rescuedPrimaryModel, tc.provider, tc.committed, tc.err)
			}
			assert.Equal(t, breaker.StateClosed, h.state())
		})
	}
}
