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

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

type fakeRescueServices struct {
	rescueServices
	rescueCredentialPort
	rescueRoutingPort
	outcomes   map[string]error
	dispatched []string
	readmitted []string
}

func (f *fakeRescueServices) resolveEffort(context.Context, router.Decision, router.ModelSpec, bool) dispatch.EffortResolution {
	return dispatch.EffortResolution{}
}

func (f *fakeRescueServices) ResolveCredentials(ctx context.Context, _, _ string, _ http.Header) context.Context {
	return ctx
}

func (f *fakeRescueServices) ResolveBindingsForDispatch(_ context.Context, d router.Decision) []catalog.ProviderBinding {
	return []catalog.ProviderBinding{{Provider: d.Provider}}
}

func (f *fakeRescueServices) NoteRescueReadmission(_ context.Context, _, rescuer router.Decision) {
	f.readmitted = append(f.readmitted, rescuer.Model)
}

func (f *fakeRescueServices) dispatchWithFallback(_ context.Context, in dispatch.FailoverInputs) (int, error) {
	f.dispatched = append(f.dispatched, in.InitialDecision.Model)
	return 0, f.outcomes[in.InitialDecision.Model]
}

func newSiblingRescueChain(ctx context.Context, svc *fakeRescueServices, primaryErr error, siblings ...router.Decision) *dispatch.RescueChain {
	primary := router.Decision{Model: "primary-model", Provider: providers.ProviderOpenAI}
	c := &dispatch.RescueChain{
		RescuePorts: dispatch.RescuePorts{
			Credentials: svc,
			Routing:     svc,
			Funding:     rescueFundingPort{},
			Attempts:    rescueAttemptPort{svc},
			Overflow:    rescueOverflowPort{},
			Dispatch:    svc.dispatchWithFallback,
		},
		Markers:     rescueMarkers{},
		HTTPRequest: httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", nil),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		PreludeBuf:  newPreludeBuffer(httptest.NewRecorder()),
		BuildAttempt: func(router.Decision, translate.EmitOptions, string, context.Context) (dispatch.UpstreamAttempt, error) {
			return func(context.Context, router.Decision, dispatch.Upstream) error { return nil }, nil
		},
		RespSummary:      &translate.ResponseSummary{StopReason: "stale"},
		ReqStats:         &providers.RequestMutationStats{},
		Decision:         primary,
		ProxyErr:         primaryErr,
		SiblingDecisions: siblings,
		SiblingViable:    len(siblings) > 0,
	}
	c.HoldPrimary()
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

	c.SiblingRescue(context.Background())

	if got, want := svc.dispatched, []string{"first-sibling", "second-sibling"}; !slices.Equal(got, want) {
		t.Fatalf("dispatched = %v, want %v", got, want)
	}
	if c.ProxyErr != nil || !c.SiblingFailoverUsed || !c.SiblingRescueRan {
		t.Fatalf("proxyErr=%v siblingFailoverUsed=%v siblingRescueRan=%v, want served by a sibling", c.ProxyErr, c.SiblingFailoverUsed, c.SiblingRescueRan)
	}
	if c.Decision.Model != "second-sibling" {
		t.Fatalf("decision = %q, want the sibling that served", c.Decision.Model)
	}
	if c.RescuedPrimary.Model != "primary-model" || !errors.Is(c.RescuedPrimaryErr, primaryErr) {
		t.Fatalf("rescuedPrimary = %q / %v, want the routed model and its error", c.RescuedPrimary.Model, c.RescuedPrimaryErr)
	}
	if c.RespSummary.StopReason != "" {
		t.Fatalf("respSummary not reset for the rescuer: %+v", *c.RespSummary)
	}
}

func TestRescueChainSiblingRescueSkipsAfterTheDeferredErrorFlushed(t *testing.T) {
	svc := &fakeRescueServices{}
	c := newSiblingRescueChain(t.Context(), svc, &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable},
		router.Decision{Model: "first-sibling", Provider: providers.ProviderAnthropic})
	c.DeferredErrFlushed = true

	c.SiblingRescue(context.Background())

	if len(svc.dispatched) != 0 || c.SiblingRescueOwed {
		t.Fatalf("dispatched = %v, owed = %v; a flushed error forecloses the sibling walk", svc.dispatched, c.SiblingRescueOwed)
	}
}

func TestRescueChainSiblingRescueRunsWithoutRequestStats(t *testing.T) {
	svc := &fakeRescueServices{}
	sibling := router.Decision{Model: "first-sibling", Provider: providers.ProviderAnthropic}
	c := newSiblingRescueChain(t.Context(), svc, &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}, sibling)
	c.ReqStats = nil

	c.SiblingRescue(context.Background())

	if want := []string{"first-sibling"}; !slices.Equal(svc.dispatched, want) {
		t.Fatalf("events = %v, want %v", svc.dispatched, want)
	}
	if c.ServedProvider("unserved") != providers.ProviderAnthropic {
		t.Fatalf("servedProvider = %q, want the binding that served", c.ServedProvider("unserved"))
	}
}
