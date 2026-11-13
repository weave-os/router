package policy

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
)

// AtomicClassifierFacts adapts already committed V3 facts to Go-owned selection.
// It performs no inference and cannot accept a caller-supplied model choice.
type AtomicClassifierFacts struct {
	Release       string
	ReleaseSHA256 string
}

// Decide refuses ordinary clipped queries without a durable prediction.
func (a AtomicClassifierFacts) Decide(_ context.Context, query Query) (Result, error) {
	prediction := query.ClassifierPrediction
	if query.Strategy != router.StrategyLLMClassifier || query.ExecutionMode != ExecutionModeServing || prediction == nil {
		return Result{}, router.ErrClassifierUnavailable
	}
	if err := prediction.Validate(); err != nil {
		return Result{}, err
	}
	escalationClasses := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
	probabilities := make(map[string]float64, len(escalationClasses))
	for index, class := range escalationClasses {
		probabilities[class] = prediction.Probabilities[index]
	}
	return Result{SchemaVersion: SchemaVersionV4, RouteID: query.RouteID,
		PolicyArtifactID: a.Release, PolicyArtifactSHA256: a.ReleaseSHA256,
		PredictedLabel: escalationClasses[prediction.Complexity], ClassOrder: escalationClasses, ClassProbabilities: probabilities}, nil
}
