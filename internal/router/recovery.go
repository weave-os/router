package router

import "weave-os/router/internal/inference"

// DispatchContext is the full-envelope context estimate used for binding authorization.
type DispatchContext struct {
	InputTokens      int
	OutputReserve    int
	SignatureSavings int
	// FitsBinding is supplied by the serving layer, which owns effective
	// provider windows and signature stripping. It never changes cost inputs.
	FitsBinding func(model, provider string) bool
}

// ServingRecovery keeps execution authorization separate from HMM evidence.
// Plans are ordered, single-model authorizations; the first is the current target.
type ServingRecovery struct {
	Plans   []inference.ResolvedPlan
	Budget  *inference.AttemptBudget
	Failure error
}
