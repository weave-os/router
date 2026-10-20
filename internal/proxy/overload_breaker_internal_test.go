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
	"weave-os/router/internal/requestcontext"
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

// A due probe goes only to a request whose sibling rescue would absorb
// another 529; every other request leaves the circuit due for the next one.
func TestOverloadBreaker_ProbeOnlyWhereRescuable(t *testing.T) {
	cases := []struct {
		name     string
		ctx      func() context.Context
		endpoint router.TranslationEndpoint
		want     bool
	}{
		{name: "messages", ctx: rescuedFailureCtx, endpoint: router.EndpointAnthropicMessages, want: true},
		{name: "chat completions", ctx: rescuedFailureCtx, endpoint: router.EndpointOpenAIChat, want: true},
		{name: "gemini never rescues", ctx: rescuedFailureCtx, endpoint: router.EndpointGeminiGenerate},
		{name: "sibling failover off", endpoint: router.EndpointAnthropicMessages, ctx: func() context.Context {
			return flags.WithOverrides(rescuedFailureCtx(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeySiblingFailover: false}})
		}},
		{name: "caller credentials", endpoint: router.EndpointAnthropicMessages, ctx: func() context.Context {
			return requestcontext.WithCredentials(rescuedFailureCtx(), &Credentials{Source: credSourceBYOK})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOverloadHarness(true)
			h.svc.overloadBreaker.RecordOverload(rescuedPrimaryModel)
			h.svc.overloadBreaker.RecordOverload(rescuedPrimaryModel)
			h.clock = h.clock.Add(30 * time.Second)
			req := router.Request{TranslationRequirements: router.TranslationRequirements{Endpoint: tc.endpoint}}

			_, release, claimed := h.svc.claimOverloadProbe(tc.ctx(), req)

			assert.Equal(t, tc.want, claimed)
			if claimed {
				release()
			}
			assert.Equal(t, breaker.StateOpen, h.state())
		})
	}
}

// A request that does not hold the probe cannot resolve the circuit: a slow
// success that started before the circuit opened leaves it half-open, and
// only the probe's own outcome closes or reopens it.
func TestRecordOverloadOutcome_OnlyTheProbeResolves(t *testing.T) {
	h := newOverloadHarness(true)
	h.svc.overloadBreaker.RecordOverload(rescuedPrimaryModel)
	h.svc.overloadBreaker.RecordOverload(rescuedPrimaryModel)
	h.clock = h.clock.Add(30 * time.Second)
	probe, _, claimed := h.svc.claimOverloadProbe(rescuedFailureCtx(), router.Request{})
	require.True(t, claimed)
	probeCtx := withOverloadProbe(rescuedFailureCtx(), probe)

	h.svc.recordOverloadOutcome(rescuedFailureCtx(), rescuedPrimaryModel, providers.ProviderAnthropic, false, nil)
	assert.Equal(t, breaker.StateHalfOpen, h.state(), "a success without the probe does not close the circuit")

	h.svc.recordOverloadOutcome(probeCtx, rescuedPrimaryModel, providers.ProviderAnthropic, false, overloaded529())
	assert.Equal(t, breaker.StateOpen, h.state(), "the probe's own 529 reopens it")
}

// An open circuit only stops the router choosing the model. When a two-model
// roster's other arm fails and nothing else is eligible, the sibling rescue
// readmits the overloaded model rather than surfacing the failure.
func TestRescueDecisions_ReadmitsOpenCircuitAsLastResort(t *testing.T) {
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"claude-sonnet-5", "claude-opus-5"},
		CandidateProviders: map[string]string{"claude-opus-5": providers.ProviderAnthropic},
	}
	failed := overloadedDecision(md)
	failed.Model = "claude-sonnet-5"
	for _, tc := range []struct {
		name    string
		enabled bool
		want    []string
	}{
		{name: "flag on readmits the open circuit", enabled: true, want: []string{"claude-opus-5"}},
		{name: "flag off has nothing withdrawn", enabled: false, want: []string{"claude-opus-5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := siblingService(providers.ProviderAnthropic).WithProviderOverloaded(tc.enabled).
				WithOverloadBreaker(breaker.New(breaker.Config{Strikes: 1, Window: time.Minute, RecoveryBase: time.Minute}, nil))
			s.overloadBreaker.RecordOverload("claude-opus-5")

			assert.Equal(t, tc.want, siblingModels(s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)))
		})
	}
}

// A healthy candidate still outranks an open circuit in the rescue walk.
func TestRescueDecisions_PrefersHealthyCandidateOverOpenCircuit(t *testing.T) {
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"claude-sonnet-5", "claude-opus-5", "claude-haiku-4-5"},
		CandidateProviders: map[string]string{"claude-opus-5": providers.ProviderAnthropic, "claude-haiku-4-5": providers.ProviderAnthropic},
	}
	failed := overloadedDecision(md)
	failed.Model = "claude-sonnet-5"
	s := siblingService(providers.ProviderAnthropic).WithProviderOverloaded(true).
		WithOverloadBreaker(breaker.New(breaker.Config{Strikes: 1, Window: time.Minute, RecoveryBase: time.Minute}, nil))
	s.overloadBreaker.RecordOverload("claude-opus-5")

	assert.Equal(t, []string{"claude-haiku-4-5"}, siblingModels(s.siblingFailoverDecisions(context.Background(), failed, 1_000, 0, 0)))
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
