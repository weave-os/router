package hmm

import "weave-os/router/internal/router"

type ClassifierTelemetry struct {
	Margin         *float64
	PolicyGroup    string
	ArtifactID     string
	ArtifactSHA256 string
	PredictedLabel string
	ClassOrder     []string
	Probabilities  map[string]float64
	SelectionTrace *router.SelectionTrace
}

func ClassifierTelemetryOf(md *router.RoutingMetadata) ClassifierTelemetry {
	if md == nil {
		return ClassifierTelemetry{}
	}
	return ClassifierTelemetry{
		Margin:         md.ClassifierMargin,
		PolicyGroup:    md.PolicyGroup,
		ArtifactID:     md.ClassifierArtifactID,
		ArtifactSHA256: md.ClassifierArtifactSHA256,
		PredictedLabel: md.ClassifierPredictedLabel,
		ClassOrder:     md.ClassifierClassOrder,
		Probabilities:  md.ClassifierProbabilities,
		SelectionTrace: md.SelectionTrace,
	}
}

func ClassifierMarginAndGroup(md *router.RoutingMetadata) (*float64, string) {
	classifier := ClassifierTelemetryOf(md)
	return classifier.Margin, classifier.PolicyGroup
}
