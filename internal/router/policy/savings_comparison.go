package policy

import "weave-os/router/internal/router"

const (
	SavingsComparisonModel           = "claude-opus-5-5"
	SavingsComparisonModelLabel      = "Claude Opus 5.5"
	autoEfficientComparisonModel     = "claude-sonnet-5-5"
	autoEfficientComparisonModelName = "Claude Sonnet 5.5"
)

func SavingsComparisonFor(model router.RoutingModel) (modelID, label string) {
	if model == router.ModelAutoEfficient {
		return autoEfficientComparisonModel, autoEfficientComparisonModelName
	}
	return SavingsComparisonModel, SavingsComparisonModelLabel
}
