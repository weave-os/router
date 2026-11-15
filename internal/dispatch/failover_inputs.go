package dispatch

import (
	"context"
	"net/http"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

// UpstreamAttempt does one per-binding dispatch. Returns the upstream error
// unmodified so dispatchWithFallback can decide whether to retry.
type UpstreamAttempt func(ctx context.Context, decision router.Decision, p Upstream) error

// FailoverInputs bundles the inputs dispatchWithFallback needs that don't
// belong to a single attempt.
type FailoverInputs struct {
	// Writer is the real client writer; FlushErr writes here directly (bypassing
	// Prelude) so the client sees the upstream error in its expected format.
	Writer http.ResponseWriter
	// Prelude is the writer per-attempt code writes through; its Committed()
	// bit gates retry. nil when failover is impossible (single binding,
	// BYOK, legacy mode) — the loop then skips discard/commit entirely.
	Prelude *PreludeBuffer
	// InitialDecision carries the model + cluster metadata from the
	// router. dispatchWithFallback rewrites Provider per-attempt.
	InitialDecision router.Decision
	// Bindings is the ordered (provider, upstream-id, price) list, filtered
	// to providers wired in this deploy. Index 0 is primary, >0 fallbacks.
	Bindings []catalog.ProviderBinding
	Attempt  UpstreamAttempt
	// FlushErr renders the final-attempt upstream error to Writer in the
	// entry point's wire format. Optional — nil means do nothing on
	// exhaustion (the error is still returned to the caller).
	FlushErr func(w http.ResponseWriter, err error)
	// DeferFlushOnExhaustion suppresses FlushErr on exhaustion (still
	// discards the buffer and returns the error), letting the caller run a
	// higher-level fallback instead (e.g. ProxyMessages' baseline failover
	// re-dispatching the Anthropic model when a routed OSS model exhausts).
	DeferFlushOnExhaustion bool
	// Purpose names the registered operation the walk is authorized under;
	// Origin names the override source that fixed the decision's model.
	Purpose                         inference.Purpose
	Origin                          policy.OverrideSource
	SubscriptionStateRequest        *router.Request
	SubscriptionStateHeaders        http.Header
	Alternatives                    []router.Decision
	BuildAlternative                func(router.Decision) (UpstreamAttempt, error)
	OnAlternative                   func(router.Decision)
	OnSubscriptionStateTarget       func(router.Decision, []catalog.ProviderBinding)
	OnSubscriptionStatePaidTarget   func(router.Decision, []catalog.ProviderBinding)
	SubscriptionStateWinnerProvider *string
}
