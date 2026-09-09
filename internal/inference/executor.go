package inference

import (
	"context"
)

// ResolvedPlan is read-only execution authorization produced by the policy
// resolver. Concrete plans keep construction fields private.
type ResolvedPlan interface {
	Purpose() Purpose
	DispatchClass() DispatchClass
	PolicyID() PolicyID
	RegistryRevision() string
	PolicyRevision() PolicyRevision
	SelectionStrategy() SelectionStrategy
	SelectedTarget() Target
	AlternativeTargets() []Target
	HardConstraints() []Constraint
	SoftPreferences() []SoftPreference
	Budget() BudgetSpec
	Provenance() PlanProvenance
}

// Executor is the single provider-inference execution boundary. The concrete
// implementation arrives with the dispatch migration; feature packages depend
// on this contract rather than provider clients or provider maps.
type Executor interface {
	Execute(context.Context, InvocationRequest, ResolvedPlan) (ExecutionOutcome, error)
}
