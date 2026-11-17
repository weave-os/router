package dispatch

import (
	"context"
	"net/http"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

type UpstreamAttempt func(ctx context.Context, decision router.Decision, p Upstream) error

type FailoverInputs struct {
	Writer                          http.ResponseWriter
	Prelude                         *PreludeBuffer
	InitialDecision                 router.Decision
	Bindings                        []catalog.ProviderBinding
	Attempt                         UpstreamAttempt
	FlushErr                        func(w http.ResponseWriter, err error)
	DeferFlushOnExhaustion          bool
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
