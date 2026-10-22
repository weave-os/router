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
	"weave-os/router/internal/router/catalog"

	"github.com/google/uuid"
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
	org     string
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
		org:     uuid.NewString(),
		router:  &overloadAwareRouter{},
		primary: &switchableClient{failWith: overloaded529()},
		sibling: &servingClient{},
		clock:   time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	}
	now := func() time.Time { return h.clock }
	registry := breaker.New(breaker.Config{Strikes: 2, Window: time.Minute, OrgConfirmations: 2, RecoveryBase: 30 * time.Second, RecoveryCap: 2 * time.Minute}, now)
	h.svc = NewService(h.router, map[string]providers.Client{
		providers.ProviderAnthropic: h.primary,
		providers.ProviderOpenAI:    h.sibling,
	}, nil, false, nil, &demotionStubPinStore{}, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}, providers.ProviderOpenAI: {}}).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil }).
		WithProviderOverloaded(enabled, registry)
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

// ctx is a request from the harness's organization; orgCtx from any other.
func (h *overloadHarness) ctx() context.Context { return orgCtx(h.org) }

func orgCtx(org string) context.Context {
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "key-"+org)
	return context.WithValue(ctx, InstallationIDContextKey{}, org)
}

// withdrawn reports whether the primary model is out of the harness
// organization's automatic selection.
func (h *overloadHarness) withdrawn() bool {
	_, withdrawn := h.svc.overloadBreaker.Withdrawn(h.org)[rescuedPrimaryModel]
	return withdrawn
}

func (h *overloadHarness) strike(org string, times int) {
	for range times {
		h.svc.overloadBreaker.RecordOverload(org, rescuedPrimaryModel, false)
	}
}

// Two overloaded turns open the circuit; the next turn routes straight to the
// rescuer and never dispatches the overloaded model.
func TestOverloadBreaker_OpensAndRoutesAround(t *testing.T) {
	h := newOverloadHarness(true)

	h.turn(t, h.ctx())
	assert.False(t, h.withdrawn())
	h.turn(t, h.ctx())
	require.True(t, h.withdrawn())
	callsWhenOpened := h.primary.calls

	rec := h.turn(t, h.ctx())

	assert.Equal(t, callsWhenOpened, h.primary.calls, "an open circuit keeps the model out of automatic selection")
	assert.Equal(t, rescuerModel, rec.Header().Get(HeaderRouterModel))
	assert.Equal(t, 3, h.sibling.calls)
}

// Each overloaded turn retries its binding in place; those retries are one
// strike, so a single bad turn cannot open the circuit by itself.
func TestOverloadBreaker_CountsTurnsNotAttempts(t *testing.T) {
	h := newOverloadHarness(true)

	h.turn(t, h.ctx())

	assert.Greater(t, h.primary.calls, 1, "the turn retried the overloaded binding")
	assert.False(t, h.withdrawn())
}

// After the recovery timeout one request probes the model; a served probe
// closes the circuit and the model routes normally again.
func TestOverloadBreaker_HalfOpenProbeClosesOnSuccess(t *testing.T) {
	h := newOverloadHarness(true)
	h.turn(t, h.ctx())
	h.turn(t, h.ctx())
	require.True(t, h.withdrawn())

	h.clock = h.clock.Add(30 * time.Second)
	h.primary.failWith = nil
	rec := h.turn(t, h.ctx())

	assert.Equal(t, rescuedPrimaryModel, rec.Header().Get(HeaderRouterModel), "the probe is routed to the recovered model")
	assert.False(t, h.withdrawn())
	assert.Equal(t, rescuedPrimaryModel, h.turn(t, h.ctx()).Header().Get(HeaderRouterModel))
}

// A probe that hits another 529 is rescued as before and reopens the circuit
// for twice as long.
func TestOverloadBreaker_FailedProbeReopensWithBackoff(t *testing.T) {
	h := newOverloadHarness(true)
	h.turn(t, h.ctx())
	h.turn(t, h.ctx())

	h.clock = h.clock.Add(30 * time.Second)
	rec := h.turn(t, h.ctx())

	assert.Equal(t, rescuerModel, rec.Header().Get(HeaderRouterModel), "the failed probe is still rescued")
	assert.True(t, h.withdrawn())
	h.clock = h.clock.Add(59 * time.Second)
	routedBefore := len(h.router.routed)
	h.turn(t, h.ctx())
	assert.Equal(t, rescuerModel, h.router.routed[routedBefore], "the second open period is 60s, not 30s")
}

// A probe slot claimed by a request that ended up serving another model is
// released when the request ends, so the next request probes instead of
// waiting out the lease.
func TestOverloadBreaker_UnexercisedProbeIsReleased(t *testing.T) {
	h := newOverloadHarness(true)
	h.turn(t, h.ctx())
	h.turn(t, h.ctx())
	h.clock = h.clock.Add(30 * time.Second)

	h.router.preferRescuer = true
	rec := h.turn(t, h.ctx())
	require.Equal(t, rescuerModel, rec.Header().Get(HeaderRouterModel))
	assert.True(t, h.withdrawn(), "the slot went back to open")

	h.router.preferRescuer = false
	h.primary.failWith = nil
	assert.Equal(t, rescuedPrimaryModel, h.turn(t, h.ctx()).Header().Get(HeaderRouterModel))
	assert.False(t, h.withdrawn())
}

// A due probe goes only to a request whose sibling rescue would absorb
// another 529; every other request leaves the circuit due for the next one.
const probeTestOrg = "6f1c6f0e-6d0a-4c9b-9a55-3a1f1f0c2b11"

func TestOverloadBreaker_ProbeOnlyWhereRescuable(t *testing.T) {
	cases := []struct {
		name     string
		ctx      func() context.Context
		endpoint router.TranslationEndpoint
		want     bool
	}{
		{name: "messages", ctx: func() context.Context { return orgCtx(probeTestOrg) }, endpoint: router.EndpointAnthropicMessages, want: true},
		{name: "chat completions", ctx: func() context.Context { return orgCtx(probeTestOrg) }, endpoint: router.EndpointOpenAIChat, want: true},
		{name: "gemini never rescues", ctx: func() context.Context { return orgCtx(probeTestOrg) }, endpoint: router.EndpointGeminiGenerate},
		{name: "sibling failover off", endpoint: router.EndpointAnthropicMessages, ctx: func() context.Context {
			return flags.WithOverrides(orgCtx(probeTestOrg), flags.Overrides{Bools: map[flags.Key]bool{flags.KeySiblingFailover: false}})
		}},
		{name: "caller credentials", endpoint: router.EndpointAnthropicMessages, ctx: func() context.Context {
			return requestcontext.WithCredentials(orgCtx(probeTestOrg), &Credentials{Source: credSourceBYOK})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOverloadHarness(true)
			h.org = probeTestOrg
			h.strike(h.org, 2)
			h.clock = h.clock.Add(30 * time.Second)
			req := router.Request{TranslationRequirements: router.TranslationRequirements{Endpoint: tc.endpoint}}

			_, release, claimed := h.svc.claimOverloadProbe(tc.ctx(), req)

			assert.Equal(t, tc.want, claimed)
			if claimed {
				release()
			}
			assert.True(t, h.withdrawn())
		})
	}
}

// A request that does not hold the probe cannot resolve the circuit: a slow
// success that started before the circuit opened leaves it half-open, and
// only the probe's own outcome closes or reopens it.
func TestRecordOverloadOutcome_OnlyTheProbeResolves(t *testing.T) {
	h := newOverloadHarness(true)
	h.strike(h.org, 2)
	h.clock = h.clock.Add(30 * time.Second)
	probe, _, claimed := h.svc.claimOverloadProbe(h.ctx(), router.Request{})
	require.True(t, claimed)
	probeCtx := withOverloadProbe(h.ctx(), probe)

	h.svc.recordOverloadOutcome(h.ctx(), rescuedPrimaryModel, providers.ProviderAnthropic, false, nil)
	assert.True(t, h.withdrawn(), "a success without the probe does not close the circuit")

	_, _, reclaimed := h.svc.claimOverloadProbe(h.ctx(), router.Request{})
	assert.False(t, reclaimed, "the probe is still held")

	h.svc.recordOverloadOutcome(probeCtx, rescuedPrimaryModel, providers.ProviderAnthropic, false, overloaded529())
	assert.True(t, h.withdrawn(), "the probe's own 529 reopens it")
	_, _, reclaimed = h.svc.claimOverloadProbe(h.ctx(), router.Request{})
	assert.False(t, reclaimed, "a reopened circuit waits out its doubled recovery")
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
			s := siblingService(providers.ProviderAnthropic).WithProviderOverloaded(tc.enabled,
				breaker.New(breaker.Config{Strikes: 1, Window: time.Minute, OrgConfirmations: 1, RecoveryBase: time.Minute}, nil))
			s.overloadBreaker.RecordOverload(probeTestOrg, "claude-opus-5", false)

			assert.Equal(t, tc.want, siblingModels(s.siblingFailoverDecisions(orgCtx(probeTestOrg), failed, 1_000, 0, 0)))
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
	s := siblingService(providers.ProviderAnthropic).WithProviderOverloaded(true,
		breaker.New(breaker.Config{Strikes: 1, Window: time.Minute, OrgConfirmations: 1, RecoveryBase: time.Minute}, nil))
	s.overloadBreaker.RecordOverload(probeTestOrg, "claude-opus-5", false)

	assert.Equal(t, []string{"claude-haiku-4-5"}, siblingModels(s.siblingFailoverDecisions(orgCtx(probeTestOrg), failed, 1_000, 0, 0)))
}

// Flag off, nothing is recorded and every turn pays the overload and rescue
// exactly as before.
func TestOverloadBreaker_FlagOffKeepsTodaysBehaviour(t *testing.T) {
	h := newOverloadHarness(false)

	for range 3 {
		h.turn(t, h.ctx())
	}

	assert.Equal(t, []string{rescuedPrimaryModel, rescuedPrimaryModel, rescuedPrimaryModel}, h.router.routed)
	assert.False(t, h.withdrawn())
}

// A per-org override turns the breaker off for that org's requests even when
// the deployment default is on.
func TestOverloadBreaker_OrgOverrideDisables(t *testing.T) {
	h := newOverloadHarness(true)
	ctx := func() context.Context {
		return flags.WithOverrides(h.ctx(), flags.Overrides{Bools: map[flags.Key]bool{flags.KeyProviderOverloaded: false}})
	}

	h.turn(t, ctx())
	h.turn(t, ctx())

	assert.False(t, h.withdrawn())
}

// An open circuit is a soft exclusion: the pin-drop log names why, and the
// reason is reported alongside operator disables.
func TestOverloadBreaker_ReportsReason(t *testing.T) {
	h := newOverloadHarness(true)
	h.turn(t, h.ctx())
	h.turn(t, h.ctx())

	reason, disabled := h.svc.globalAutomaticExclusionReason(h.ctx(), rescuedPrimaryModel)

	assert.True(t, disabled)
	assert.Equal(t, overloadBreakerReason, reason)
}

// Committed streams, client cancellations, non-529 failures and requests with
// no organization are not evidence of model overload.
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOverloadHarness(true)
			for range 3 {
				h.svc.recordOverloadOutcome(h.ctx(), rescuedPrimaryModel, tc.provider, tc.committed, tc.err)
			}
			assert.False(t, h.withdrawn())
		})
	}
	t.Run("no organization", func(t *testing.T) {
		h := newOverloadHarness(true)
		for range 3 {
			h.svc.recordOverloadOutcome(context.Background(), rescuedPrimaryModel, providers.ProviderAnthropic, false, overloaded529())
		}
		assert.Empty(t, h.svc.overloadBreaker.Withdrawn(""), "failures without an organization must not reach the registry")
	})
}

// Weave's own overloads open the shared circuit, but Musinsa keeps being
// served by the model until it has seen its own overloads; then it routes
// around the model without waiting for a full threshold of its own.
func TestOverloadBreaker_OtherOrganizationsMustConfirm(t *testing.T) {
	h := newOverloadHarness(true)
	musinsa := uuid.NewString()
	h.turn(t, h.ctx())
	h.turn(t, h.ctx())
	require.True(t, h.withdrawn())

	h.primary.failWith = nil
	callsBefore := h.primary.calls
	rec := h.turn(t, orgCtx(musinsa))
	assert.Equal(t, rescuedPrimaryModel, rec.Header().Get(HeaderRouterModel), "an unconfirmed organization is still served by the model")
	assert.Equal(t, callsBefore+1, h.primary.calls)
	assert.Equal(t, rescuerModel, h.turn(t, h.ctx()).Header().Get(HeaderRouterModel), "the confirmed organization routes around it")

	h.primary.failWith = overloaded529()
	h.turn(t, orgCtx(musinsa))
	h.turn(t, orgCtx(musinsa))
	callsConfirmed := h.primary.calls
	assert.Equal(t, rescuerModel, h.turn(t, orgCtx(musinsa)).Header().Get(HeaderRouterModel))
	assert.Equal(t, callsConfirmed, h.primary.calls, "once confirmed, Musinsa no longer dispatches the overloaded model")
}

// A served probe from any organization closes the shared circuit for all of
// them.
func TestOverloadBreaker_SharedRecoveryClearsEveryOrganization(t *testing.T) {
	h := newOverloadHarness(true)
	musinsa := uuid.NewString()
	h.turn(t, h.ctx())
	h.turn(t, h.ctx())
	h.turn(t, orgCtx(musinsa))
	h.turn(t, orgCtx(musinsa))
	require.Contains(t, h.svc.overloadBreaker.Withdrawn(musinsa), rescuedPrimaryModel)

	h.clock = h.clock.Add(30 * time.Second)
	h.primary.failWith = nil
	h.turn(t, orgCtx(musinsa))

	assert.False(t, h.withdrawn())
	assert.Equal(t, rescuedPrimaryModel, h.turn(t, h.ctx()).Header().Get(HeaderRouterModel))
}

// A gateway organization's own overloads open a circuit only it sees, and
// never touch the shared circuit other organizations read.
func TestOverloadBreaker_GatewayOrganizationIsIsolated(t *testing.T) {
	h := newOverloadHarness(true)
	cortex := uuid.NewString()
	for range 3 {
		h.svc.recordOverloadOutcome(orgCtx(cortex), rescuedPrimaryModel, providers.ProviderAnthropicGateway, false, overloaded529())
	}

	assert.Contains(t, h.svc.overloadBreaker.Withdrawn(cortex), rescuedPrimaryModel)
	assert.Contains(t, h.svc.overloadOpenModels(orgCtx(cortex)), rescuedPrimaryModel)
	assert.False(t, h.withdrawn(), "gateway failures are not shared evidence")
	assert.Empty(t, h.svc.overloadOpenModels(h.ctx()))

	for range 3 {
		h.svc.recordOverloadOutcome(h.ctx(), rescuedPrimaryModel, providers.ProviderAnthropic, false, overloaded529())
	}
	require.True(t, h.withdrawn())
	assert.Equal(t, map[string]struct{}{rescuedPrimaryModel: {}}, h.svc.overloadOpenModels(orgCtx(cortex)), "shared evidence does not confirm for the gateway organization")
	assert.Contains(t, h.svc.overloadBreaker.Withdrawn(cortex), rescuedPrimaryModel)
}

// The Snowflake shape: a two-model gateway roster whose overloaded arm is
// withdrawn still serves the turn through it when the other arm fails.
func TestOverloadBreaker_GatewayTwoModelRosterStillServes(t *testing.T) {
	primary := &failingClient{err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}}
	sibling := &fakeClient{outcomes: []fakeOutcome{{writeBytes: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"model\":\"" + recoveryOpusModel + "\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}}}
	decision := router.Decision{Provider: providers.ProviderOpenAIGateway, Model: catalog.ModelGPT6Luna, Metadata: &router.RoutingMetadata{
		RosterFailover:  true,
		RescueModels:    []string{catalog.ModelGPT6Luna, recoveryOpusModel},
		CandidateModels: []string{catalog.ModelGPT6Luna, recoveryOpusModel},
	}}
	registry := breaker.New(breaker.Config{Strikes: 1, Window: time.Minute, OrgConfirmations: 1, RecoveryBase: time.Hour}, nil)
	svc := NewService(staticRouter{decision: decision}, map[string]providers.Client{
		providers.ProviderOpenAIGateway:    primary,
		providers.ProviderAnthropicGateway: sibling,
	}, nil, false, nil, &cooldownStubPinStore{}, false, providers.ProviderAnthropic, recoveryOpusModel, nil).
		WithRetrySleep(func(context.Context, time.Duration) error { return nil }).
		WithProviderOverloaded(true, registry)
	ctx := twoModelGatewayContext()
	registry.RecordOverload(installationIDFromContext(ctx).String(), recoveryOpusModel, true)
	require.Contains(t, svc.overloadOpenModels(ctx), recoveryOpusModel)

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"gpt-6-luna","stream":true,"messages":[{"role":"user","content":"synthetic recovery check"}]}`)
	require.NoError(t, svc.ProxyMessages(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil)))

	assert.Equal(t, recoveryOpusModel, rec.Header().Get(HeaderRouterModel))
	assert.Equal(t, 1, sibling.calls)
}

// A gateway organization probes its own circuit: its rescue walks its own
// gateway aliases, so a failed probe is still absorbed.
func TestOverloadBreaker_GatewayOrganizationProbesItsOwnCircuit(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	registry := breaker.New(breaker.Config{Strikes: 1, Window: time.Minute, OrgConfirmations: 1, RecoveryBase: 30 * time.Second}, func() time.Time { return now })
	svc := NewService(nil, nil, nil, false, nil, nil, false, providers.ProviderAnthropic, recoveryOpusModel, nil).
		WithProviderOverloaded(true, registry)
	ctx := twoModelGatewayContext()
	registry.RecordOverload(installationIDFromContext(ctx).String(), recoveryOpusModel, true)
	now = now.Add(30 * time.Second)

	probe, release, claimed := svc.claimOverloadProbe(ctx, router.Request{TranslationRequirements: router.TranslationRequirements{Endpoint: router.EndpointAnthropicMessages}})

	require.True(t, claimed)
	assert.Equal(t, recoveryOpusModel, probe.Model)
	release()
}
