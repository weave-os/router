package postgres

import (
	"weave-os/router/internal/analytics"
	randomrouter "weave-os/router/internal/random_router"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/sqlc"
)

func enrichRandomBenchmarkDecision(row sqlc.GetRoutingDecisionsForExportRow, decision analytics.Decision) analytics.Decision {
	decision.APIKeyID = uuidStringPtr(row.APIKeyID)
	decision.Propensity = row.Propensity
	decision.PlannerOutcome = row.PlannerOutcome
	decision.InferenceAttemptModels = row.InferenceAttemptModels
	if row.PolicyArtifactID != nil && *row.PolicyArtifactID == randomrouter.PolicyID {
		decision.InferenceAttemptCount = &row.InferenceAttemptCount
		if row.Strategy != nil && *row.Strategy == string(router.StrategyRandomBenchmark) && row.CredentialSource != nil && *row.CredentialSource == requestcontext.SourceCodexSubscription {
			decision.ActualInputCostUSD = servedCostUSD(row.ActualInputCostUsd, true)
			decision.ActualOutputCostUSD = servedCostUSD(row.ActualOutputCostUsd, true)
		}
	}
	return decision
}
