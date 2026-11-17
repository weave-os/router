package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

type fakeRescueServices struct {
	rescueServices
	outcomes   map[string]error
	dispatched []string
	readmitted []string
}

func (f *fakeRescueServices) resolveEffort(context.Context, router.Decision, router.ModelSpec, bool) effortResolution {
	return effortResolution{}
}

func (f *fakeRescueServices) resolveCredentials(ctx context.Context, _, _ string, _ http.Header) context.Context {
	return ctx
}

func (f *fakeRescueServices) resolveBindingsForDispatch(_ context.Context, d router.Decision) []catalog.ProviderBinding {
	return []catalog.ProviderBinding{{Provider: d.Provider}}
}

func (f *fakeRescueServices) noteRescueReadmission(_ context.Context, _, rescuer router.Decision) {
	f.readmitted = append(f.readmitted, rescuer.Model)
}

func (f *fakeRescueServices) dispatchWithFallback(_ context.Context, in failoverInputs) (int, error) {
	f.dispatched = append(f.dispatched, in.initialDecision.Model)
	return 0, f.outcomes[in.initialDecision.Model]
}

func newSiblingRescueChain(ctx context.Context, svc rescueServices, primaryErr error, siblings ...router.Decision) *rescueChain {
	primary := router.Decision{Model: "primary-model", Provider: providers.ProviderOpenAI}
	c := &rescueChain{
		svc:        svc,
		r:          httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", nil),
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		preludeBuf: newPreludeBuffer(httptest.NewRecorder()),
		buildAttempt: func(router.Decision, translate.EmitOptions, string, context.Context) (dispatchAttempt, error) {
			return func(context.Context, router.Decision, providers.Client) error { return nil }, nil
		},
		respSummary:      &translate.ResponseSummary{StopReason: "stale"},
		reqStats:         &providers.RequestMutationStats{},
		decision:         primary,
		proxyErr:         primaryErr,
		siblingDecisions: siblings,
		siblingViable:    len(siblings) > 0,
	}
	c.holdPrimary()
	return c
}

func TestRescueChainSiblingRescueWalksPastAFailedRescuer(t *testing.T) {
	primaryErr := &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}
	svc := &fakeRescueServices{outcomes: map[string]error{
		"first-sibling": &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable},
	}}
	first := router.Decision{Model: "first-sibling", Provider: providers.ProviderAnthropic}
	second := router.Decision{Model: "second-sibling", Provider: providers.ProviderGoogle}
	c := newSiblingRescueChain(t.Context(), svc, primaryErr, first, second)

	c.siblingRescue(context.Background())

	if got, want := svc.dispatched, []string{"first-sibling", "second-sibling"}; !slices.Equal(got, want) {
		t.Fatalf("dispatched = %v, want %v", got, want)
	}
	if c.proxyErr != nil || !c.siblingFailoverUsed || !c.siblingRescueRan {
		t.Fatalf("proxyErr=%v siblingFailoverUsed=%v siblingRescueRan=%v, want served by a sibling", c.proxyErr, c.siblingFailoverUsed, c.siblingRescueRan)
	}
	if c.decision.Model != "second-sibling" {
		t.Fatalf("decision = %q, want the sibling that served", c.decision.Model)
	}
	if c.rescuedPrimary.Model != "primary-model" || !errors.Is(c.rescuedPrimaryErr, primaryErr) {
		t.Fatalf("rescuedPrimary = %q / %v, want the routed model and its error", c.rescuedPrimary.Model, c.rescuedPrimaryErr)
	}
	if c.respSummary.StopReason != "" {
		t.Fatalf("respSummary not reset for the rescuer: %+v", *c.respSummary)
	}
}

func TestRescueChainSiblingRescueSkipsAfterTheDeferredErrorFlushed(t *testing.T) {
	svc := &fakeRescueServices{}
	c := newSiblingRescueChain(t.Context(), svc, &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable},
		router.Decision{Model: "first-sibling", Provider: providers.ProviderAnthropic})
	c.deferredErrFlushed = true

	c.siblingRescue(context.Background())

	if len(svc.dispatched) != 0 || c.siblingRescueOwed {
		t.Fatalf("dispatched = %v, owed = %v; a flushed error forecloses the sibling walk", svc.dispatched, c.siblingRescueOwed)
	}
}

func TestRescueChainSiblingRescueRunsWithoutRequestStats(t *testing.T) {
	svc := &fakeRescueServices{}
	sibling := router.Decision{Model: "first-sibling", Provider: providers.ProviderAnthropic}
	c := newSiblingRescueChain(t.Context(), svc, &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}, sibling)
	c.reqStats = nil

	c.siblingRescue(context.Background())

	if want := []string{"first-sibling"}; !slices.Equal(svc.dispatched, want) {
		t.Fatalf("events = %v, want %v", svc.dispatched, want)
	}
	if c.servedProvider("unserved") != providers.ProviderAnthropic {
		t.Fatalf("servedProvider = %q, want the binding that served", c.servedProvider("unserved"))
	}
}
