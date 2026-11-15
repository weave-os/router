package dispatch

import (
	"context"
	"log/slog"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

// EarlyStopRedo carries what ProxyMessages lends a re-serve: the gate holding
// the stopped response, the closures that build and dispatch the candidate,
// and the served-attempt state the redo replaces (or restores on failure).
type EarlyStopRedo struct {
	Gate     *EarlyStopGate
	Prelude  *PreludeBuffer
	Header   http.Header
	Log      *slog.Logger
	Target   router.Decision
	Build    func(target router.Decision) (UpstreamAttempt, []catalog.ProviderBinding, string, EffortResolution, context.Context, error)
	Dispatch func(ctx context.Context, target router.Decision, bindings []catalog.ProviderBinding, attempt UpstreamAttempt) (int, error)
	Served   *EarlyStopServed
}

// EarlyStopServed points at ProxyMessages' record of the attempt that served.
type EarlyStopServed struct {
	Decision   *router.Decision
	Bindings   *[]catalog.ProviderBinding
	Marker     *string
	WinnerIdx  *int
	Extractor  **telemetry.UsageExtractor
	Summary    *translate.ResponseSummary
	ReqStats   *providers.RequestMutationStats
	FastServed *bool
	Effort     *EffortResolution
}

type EarlyStopServedSnapshot struct {
	Decision   router.Decision
	Bindings   []catalog.ProviderBinding
	Marker     string
	WinnerIdx  int
	Extractor  *telemetry.UsageExtractor
	Summary    translate.ResponseSummary
	ReqStats   providers.RequestMutationStats
	FastServed bool
	Effort     EffortResolution
}

func (p *EarlyStopServed) Save() EarlyStopServedSnapshot {
	return EarlyStopServedSnapshot{
		Decision: *p.Decision, Bindings: *p.Bindings, Marker: *p.Marker, WinnerIdx: *p.WinnerIdx,
		Extractor: *p.Extractor, Summary: *p.Summary, ReqStats: *p.ReqStats, FastServed: *p.FastServed, Effort: *p.Effort,
	}
}

func (p *EarlyStopServed) Restore(s EarlyStopServedSnapshot) {
	*p.Decision, *p.Bindings, *p.Marker, *p.WinnerIdx = s.Decision, s.Bindings, s.Marker, s.WinnerIdx
	*p.Extractor, *p.Summary, *p.ReqStats, *p.FastServed, *p.Effort = s.Extractor, s.Summary, s.ReqStats, s.FastServed, s.Effort
}
