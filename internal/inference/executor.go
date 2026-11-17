package inference

import (
	"context"
)

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

type Executor interface {
	Execute(context.Context, InvocationRequest, ResolvedPlan) (ExecutionOutcome, error)
}
